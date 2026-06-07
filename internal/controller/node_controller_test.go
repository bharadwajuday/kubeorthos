/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
	"kubeorthos/internal/constants"
)

var _ = Describe("Node Controller", func() {
	Context("When reconciling a Node", func() {
		const nodeName = "test-node-controller"
		const ruleName = "test-node-rule"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: nodeName,
		}

		BeforeEach(func() {
			// Clean up nodes
			var nodes corev1.NodeList
			Expect(k8sClient.List(ctx, &nodes)).To(Succeed())
			for _, node := range nodes.Items {
				_ = k8sClient.Delete(ctx, &node)
			}

			// Clean up ClusterRules
			var rules auditv1alpha1.ClusterRuleList
			Expect(k8sClient.List(ctx, &rules)).To(Succeed())
			for _, rule := range rules.Items {
				_ = k8sClient.Delete(ctx, &rule)
			}
		})

		AfterEach(func() {
			// Cleanup node
			node := &corev1.Node{}
			err := k8sClient.Get(ctx, typeNamespacedName, node)
			if err == nil {
				Expect(k8sClient.Delete(ctx, node)).To(Succeed())
			}

			// Cleanup rule
			rule := &auditv1alpha1.ClusterRule{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: ruleName}, rule)
			if err == nil {
				Expect(k8sClient.Delete(ctx, rule)).To(Succeed())
			}
		})

		It("should stamp compliant label when node complies with ClusterRule", func() {
			// Create a ClusterRule expecting a specific KubeletVersion
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: ruleName,
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.35.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Create a Node that matches and complies with KubeletVersion
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			node.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.35.0",
				},
			}
			Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Fetch the Node and check compliance labels
			Expect(k8sClient.Get(ctx, typeNamespacedName, node)).To(Succeed())
			Expect(node.Labels).To(HaveKeyWithValue(constants.LabelCompliancePrefix+ruleName, "true"))
		})

		It("should stamp non-compliant label and quarantine worker node if rule action is Enforce", func() {
			// Create an Enforce ClusterRule
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: ruleName,
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.35.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Create a non-compliant Node
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			node.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.15.0-outdated",
				},
			}
			Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify Node has non-compliant label, is cordoned, and annotated
			Expect(k8sClient.Get(ctx, typeNamespacedName, node)).To(Succeed())
			Expect(node.Labels).To(HaveKeyWithValue(constants.LabelCompliancePrefix+ruleName, "false"))
			Expect(node.Spec.Unschedulable).To(BeTrue())
			Expect(node.Annotations).To(HaveKeyWithValue(constants.AnnotationQuarantinePrefix+ruleName, "true"))
			Expect(node.Annotations).To(HaveKeyWithValue(constants.AnnotationQuarantined, "true"))
		})

		It("should clean up rule labels and quarantine annotations if node selector no longer targets node", func() {
			// Create a ClusterRule targeting "role=worker"
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: ruleName,
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"role": "worker"},
					},
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.35.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Create a Node that previously had compliance label and quarantine annotations for the rule
			// but currently DOES NOT have the "role=worker" label (so it's no longer targeted)
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Labels: map[string]string{
						constants.LabelCompliancePrefix + ruleName: "false",
					},
					Annotations: map[string]string{
						constants.AnnotationQuarantinePrefix + ruleName: "true",
						constants.AnnotationQuarantined:                 "true",
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			node.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.15.0-outdated",
				},
			}
			Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify the Node has been cleaned of the rule label and quarantine annotations, and uncordoned
			Expect(k8sClient.Get(ctx, typeNamespacedName, node)).To(Succeed())
			Expect(node.Labels).NotTo(HaveKey(constants.LabelCompliancePrefix + ruleName))
			Expect(node.Annotations).NotTo(HaveKey(constants.AnnotationQuarantinePrefix + ruleName))
			Expect(node.Annotations).NotTo(HaveKey(constants.AnnotationQuarantined))
			Expect(node.Spec.Unschedulable).To(BeFalse())
		})
	})
})

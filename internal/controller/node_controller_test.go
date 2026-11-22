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
	"github.com/prometheus/client_golang/prometheus/testutil"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
	"kubeorthos/internal/constants"
	"kubeorthos/internal/utils"
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

			// Clean up ClusterRules (strip finalizers if any)
			var rules auditv1alpha1.ClusterRuleList
			Expect(k8sClient.List(ctx, &rules)).To(Succeed())
			for i := range rules.Items {
				r := &rules.Items[i]
				if len(r.Finalizers) > 0 {
					orig := r.DeepCopy()
					r.Finalizers = nil
					_ = k8sClient.Patch(ctx, r, client.MergeFrom(orig))
				}
				_ = k8sClient.Delete(ctx, r)
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

		It("should completely ignore control-plane nodes and not stamp labels/annotations", func() {
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

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Labels: map[string]string{
						constants.LabelNodeRoleControlPlane: "",
					},
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

			Expect(k8sClient.Get(ctx, typeNamespacedName, node)).To(Succeed())
			Expect(node.Labels).NotTo(HaveKey(constants.LabelCompliancePrefix + ruleName))
			Expect(node.Annotations).NotTo(HaveKey(constants.AnnotationQuarantinePrefix + ruleName))
			Expect(node.Spec.Unschedulable).To(BeFalse())
		})

		It("should actively add and remove compliance labels and custom labels on matching nodes", func() {
			labelKey := "policy.kubeorthos.io/compliant"
			labelVal := "true"
			customKey := "environment"
			customVal := "production"

			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: ruleName,
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
					ComplianceLabel: &auditv1alpha1.ComplianceLabelSpec{
						Key:   labelKey,
						Value: labelVal,
					},
					CustomLabels: map[string]string{
						customKey: customVal,
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Node 1: Compliant node missing the labels (should be applied)
			compliantNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "compliant-node-label",
				},
			}
			Expect(k8sClient.Create(ctx, compliantNode)).To(Succeed())
			compliantNode.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.0"},
			}
			Expect(k8sClient.Status().Update(ctx, compliantNode)).To(Succeed())

			// Node 2: Non-compliant node carrying the labels (should be removed)
			nonCompliantNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "non-compliant-node-label",
					Labels: map[string]string{
						labelKey:  labelVal,
						customKey: customVal,
					},
				},
			}
			Expect(k8sClient.Create(ctx, nonCompliantNode)).To(Succeed())
			nonCompliantNode.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.15.0-outdated"},
			}
			Expect(k8sClient.Status().Update(ctx, nonCompliantNode)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "compliant-node-label"},
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = nodeReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "non-compliant-node-label"},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "compliant-node-label"}, compliantNode)).To(Succeed())
			Expect(compliantNode.Labels).To(HaveKeyWithValue(labelKey, labelVal))
			Expect(compliantNode.Labels).To(HaveKeyWithValue(customKey, customVal))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "non-compliant-node-label"}, nonCompliantNode)).To(Succeed())
			Expect(nonCompliantNode.Labels).NotTo(HaveKey(labelKey))
			Expect(nonCompliantNode.Labels).NotTo(HaveKey(customKey))
		})

		It("should uncordon worker node when it becomes compliant under ActionEnforce", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: ruleName,
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			workerCompliantQuarantined := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Annotations: map[string]string{
						constants.AnnotationQuarantinePrefix + ruleName: constants.ValueTrue,
						constants.AnnotationQuarantined:                 constants.ValueTrue,
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
			}
			Expect(k8sClient.Create(ctx, workerCompliantQuarantined)).To(Succeed())
			workerCompliantQuarantined.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.0"},
			}
			Expect(k8sClient.Status().Update(ctx, workerCompliantQuarantined)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, workerCompliantQuarantined)).To(Succeed())
			Expect(workerCompliantQuarantined.Spec.Unschedulable).To(BeFalse())
			Expect(workerCompliantQuarantined.Annotations).NotTo(HaveKey(constants.AnnotationQuarantinePrefix + ruleName))
			Expect(workerCompliantQuarantined.Annotations).NotTo(HaveKey(constants.AnnotationQuarantined))
		})

		It("should uncordon worker nodes when a rule action transitions from Enforce to Audit", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: ruleName,
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			quarantinedWorker := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Annotations: map[string]string{
						constants.AnnotationQuarantinePrefix + ruleName: constants.ValueTrue,
						constants.AnnotationQuarantined:                 constants.ValueTrue,
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
			}
			Expect(k8sClient.Create(ctx, quarantinedWorker)).To(Succeed())
			quarantinedWorker.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.15.0-outdated"},
			}
			Expect(k8sClient.Status().Update(ctx, quarantinedWorker)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			// Reconcile once in Enforce: remains quarantined
			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, quarantinedWorker)).To(Succeed())
			Expect(quarantinedWorker.Spec.Unschedulable).To(BeTrue())
			Expect(quarantinedWorker.Annotations).To(HaveKeyWithValue(constants.AnnotationQuarantined, "true"))

			// Change rule Action to Audit
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ruleName}, rule)).To(Succeed())
			rule.Spec.Action = auditv1alpha1.ActionAudit
			Expect(k8sClient.Update(ctx, rule)).To(Succeed())

			// Reconcile again: should be uncordoned and quarantine annotations removed
			_, err = nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, quarantinedWorker)).To(Succeed())
			Expect(quarantinedWorker.Spec.Unschedulable).To(BeFalse())
			Expect(quarantinedWorker.Annotations).NotTo(HaveKey(constants.AnnotationQuarantined))
		})

		It("should trigger automated resource reclamation when node experiences DiskPressure", func() {
			reclaimRuleName := "test-reclaim-rule"
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: reclaimRuleName,
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
					Reclamation: &auditv1alpha1.ReclamationSpec{
						DiskPressure: &auditv1alpha1.DiskPressureReclamation{
							CleanImages:     utils.BoolPtr(true),
							CleanContainers: utils.BoolPtr(true),
							CleanLogs:       utils.BoolPtr(true),
							LogSizeLimit:    "100Mi",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, rule) }()

			pressuredNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pressured-worker-node",
				},
			}
			Expect(k8sClient.Create(ctx, pressuredNode)).To(Succeed())
			pressuredNode.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.0"},
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeDiskPressure,
						Status: corev1.ConditionTrue,
					},
				},
			}
			Expect(k8sClient.Status().Update(ctx, pressuredNode)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			// 1. Reconcile: should spawn the Job
			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "pressured-worker-node"},
			})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "reclaim-test-reclaim-rule-pressured-worker-node", Namespace: "default"}, job)).To(Succeed())
			Expect(job.Spec.Template.Spec.NodeName).To(Equal("pressured-worker-node"))
			Expect(job.Spec.Template.Spec.Containers[0].SecurityContext.Privileged).To(BeNil())
			Expect(job.Spec.Template.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation).To(Equal(utils.BoolPtr(false)))
			Expect(job.Spec.Template.Spec.Containers[0].SecurityContext.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
			Expect(job.Spec.Template.Spec.Containers[0].SecurityContext.Capabilities.Add).To(ContainElement(corev1.Capability("DAC_OVERRIDE")))

			// 2. Mark Job as Succeeded and reconcile: should delete the Job
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			_, err = nodeReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "pressured-worker-node"},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "reclaim-test-reclaim-rule-pressured-worker-node", Namespace: "default"}, job)
				g.Expect(apierrors.IsNotFound(err) || job.DeletionTimestamp != nil).To(BeTrue())
			}, "10s").Should(Succeed())
		})

		It("should safely handle multiple overlapping ClusterRules under Scoped Quarantining", func() {
			ruleAName := "test-rule-a"
			ruleBName := "test-rule-b"

			// Rule A expects v1.34.0 (node will be compliant with A)
			ruleA := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{Name: ruleAName},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ruleA)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, ruleA) }()

			// Rule B expects v1.35.0 (node is initially non-compliant with B)
			ruleB := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{Name: ruleBName},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.35.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ruleB)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, ruleB) }()

			sharedNodeName := "worker-shared-quarantine"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: sharedNodeName,
					Annotations: map[string]string{
						constants.AnnotationQuarantinePrefix + ruleAName: "true",
						constants.AnnotationQuarantinePrefix + ruleBName: "true",
						constants.AnnotationQuarantined:                  "true",
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			node.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.0"},
			}
			Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			// 1. Reconcile: compliant with Rule A, non-compliant with Rule B -> Rule A claim released, node stays cordoned
			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: sharedNodeName},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sharedNodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue())
			Expect(node.Annotations).NotTo(HaveKey(constants.AnnotationQuarantinePrefix + ruleAName))
			Expect(node.Annotations).To(HaveKeyWithValue(constants.AnnotationQuarantinePrefix+ruleBName, "true"))
			Expect(node.Annotations).To(HaveKeyWithValue(constants.AnnotationQuarantined, "true"))

			// 2. Make Rule B also expect v1.34.0 so node becomes compliant with both rules
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ruleBName}, ruleB)).To(Succeed())
			ruleB.Spec.ExpectedNodeConfig.KubeletVersion = "v1.34.0"
			Expect(k8sClient.Update(ctx, ruleB)).To(Succeed())

			_, err = nodeReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: sharedNodeName},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sharedNodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeFalse())
			Expect(node.Annotations).NotTo(HaveKey(constants.AnnotationQuarantinePrefix + ruleBName))
			Expect(node.Annotations).NotTo(HaveKey(constants.AnnotationQuarantined))
		})

		It("should clean up orphaned compliance labels and quarantine claims for deleted rules", func() {
			// No ClusterRule exists for "deleted-rule"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Labels: map[string]string{
						constants.LabelCompliancePrefix + "deleted-rule": "false",
					},
					Annotations: map[string]string{
						constants.AnnotationQuarantinePrefix + "deleted-rule": constants.ValueTrue,
						constants.AnnotationQuarantined:                       constants.ValueTrue,
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, node)).To(Succeed())
			Expect(node.Labels).NotTo(HaveKey(constants.LabelCompliancePrefix + "deleted-rule"))
			Expect(node.Annotations).NotTo(HaveKey(constants.AnnotationQuarantinePrefix + "deleted-rule"))
			Expect(node.Annotations).NotTo(HaveKey(constants.AnnotationQuarantined))
			Expect(node.Spec.Unschedulable).To(BeFalse())
		})

		It("should treat terminating ClusterRules as inactive and clean up their node state", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:       ruleName,
					Finalizers: []string{constants.ClusterRuleFinalizer},
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.35.0",
					},
					ComplianceLabel: &auditv1alpha1.ComplianceLabelSpec{
						Key:   "policy.kubeorthos.io/compliant",
						Value: "true",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())
			// Mark rule for deletion (stays in etcd because of finalizer)
			Expect(k8sClient.Delete(ctx, rule)).To(Succeed())
			defer func() {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: ruleName}, rule); err == nil {
					orig := rule.DeepCopy()
					controllerutil.RemoveFinalizer(rule, constants.ClusterRuleFinalizer)
					_ = k8sClient.Patch(ctx, rule, client.MergeFrom(orig))
				}
			}()

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Labels: map[string]string{
						constants.LabelCompliancePrefix + ruleName: "false",
						"policy.kubeorthos.io/compliant":           "true",
					},
					Annotations: map[string]string{
						constants.AnnotationQuarantinePrefix + ruleName: constants.ValueTrue,
						constants.AnnotationQuarantined:                 constants.ValueTrue,
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			node.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.15.0-outdated"},
			}
			Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, node)).To(Succeed())
			Expect(node.Labels).NotTo(HaveKey(constants.LabelCompliancePrefix + ruleName))
			Expect(node.Labels).NotTo(HaveKey("policy.kubeorthos.io/compliant"))
			Expect(node.Annotations).NotTo(HaveKey(constants.AnnotationQuarantinePrefix + ruleName))
			Expect(node.Annotations).NotTo(HaveKey(constants.AnnotationQuarantined))
			Expect(node.Spec.Unschedulable).To(BeFalse())
		})
	})

	Describe("Node Reconcile Predicate Filter", func() {
		var pred nodeReconcilePredicate

		BeforeEach(func() {
			pred = nodeReconcilePredicate{}
		})

		It("should approve Create for fresh nodes and ignore Delete and Generic events", func() {
			Expect(pred.Create(event.CreateEvent{Object: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "fresh-node"}}})).To(BeTrue())
			Expect(pred.Create(event.CreateEvent{Object: nil})).To(BeFalse())
			Expect(pred.Delete(event.DeleteEvent{Object: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "deleted-node"}}})).To(BeFalse())
			Expect(pred.Generic(event.GenericEvent{Object: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "generic-node"}}})).To(BeFalse())
		})

		It("should ignore heartbeat-only timestamp updates and unrelated annotations", func() {
			oldNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "worker-1",
					Labels: map[string]string{"role": "worker"},
					Annotations: map[string]string{
						"flannel.alpha.coreos.com/backend-data": "abc",
					},
				},
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.0"},
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionTrue,
							LastHeartbeatTime:  metav1.Unix(100, 0),
							LastTransitionTime: metav1.Unix(100, 0),
						},
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("4"),
					},
				},
			}

			// Heartbeat timestamp update only
			heartbeatNode := oldNode.DeepCopy()
			heartbeatNode.Status.Conditions[0].LastHeartbeatTime = metav1.Unix(200, 0)
			heartbeatNode.Status.Conditions[0].LastTransitionTime = metav1.Unix(200, 0)
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: heartbeatNode})).To(BeFalse())

			// Unrelated annotation update
			unrelatedAnnNode := oldNode.DeepCopy()
			unrelatedAnnNode.Annotations["volumes.kubernetes.io/controller-managed-attach-detach"] = "true"
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: unrelatedAnnNode})).To(BeFalse())
		})

		It("should trigger on genuine Node spec, label, quarantine annotation, NodeInfo, Allocatable, and condition status changes", func() {
			oldNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "worker-1",
					Labels: map[string]string{"role": "worker"},
				},
				Spec: corev1.NodeSpec{Unschedulable: false},
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.0"},
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("4"),
					},
				},
			}

			// Unschedulable change
			n1 := oldNode.DeepCopy()
			n1.Spec.Unschedulable = true
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: n1})).To(BeTrue())

			// Label change
			n2 := oldNode.DeepCopy()
			n2.Labels["tier"] = "backend"
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: n2})).To(BeTrue())

			// Quarantine annotation change
			n3 := oldNode.DeepCopy()
			n3.Annotations = map[string]string{constants.AnnotationQuarantinePrefix + "rule-a": "true"}
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: n3})).To(BeTrue())

			// NodeInfo change
			n4 := oldNode.DeepCopy()
			n4.Status.NodeInfo.KubeletVersion = "v1.35.0"
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: n4})).To(BeTrue())

			// Allocatable change
			n5 := oldNode.DeepCopy()
			n5.Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("8")
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: n5})).To(BeTrue())

			// Condition status flip
			n6 := oldNode.DeepCopy()
			n6.Status.Conditions[0].Status = corev1.ConditionFalse
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: n6})).To(BeTrue())

			// New condition added (e.g. DiskPressure)
			n7 := oldNode.DeepCopy()
			n7.Status.Conditions = append(n7.Status.Conditions, corev1.NodeCondition{
				Type:   corev1.NodeDiskPressure,
				Status: corev1.ConditionTrue,
			})
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: n7})).To(BeTrue())
		})
	})

	Describe("Node Reconciler Metrics Verification", func() {
		const metricsRuleName = "node-metrics-rule"
		ctx := context.Background()

		BeforeEach(func() {
			var nodes corev1.NodeList
			Expect(k8sClient.List(ctx, &nodes)).To(Succeed())
			for _, node := range nodes.Items {
				_ = k8sClient.Delete(ctx, &node)
			}
			var rules auditv1alpha1.ClusterRuleList
			Expect(k8sClient.List(ctx, &rules)).To(Succeed())
			for _, rule := range rules.Items {
				_ = k8sClient.Delete(ctx, &rule)
			}
		})

		AfterEach(func() {
			var nodes corev1.NodeList
			Expect(k8sClient.List(ctx, &nodes)).To(Succeed())
			for _, node := range nodes.Items {
				_ = k8sClient.Delete(ctx, &node)
			}
			var rules auditv1alpha1.ClusterRuleList
			Expect(k8sClient.List(ctx, &rules)).To(Succeed())
			for _, rule := range rules.Items {
				_ = k8sClient.Delete(ctx, &rule)
			}
		})

		It("should increment nodes evaluated, compliant and non-compliant counters correctly", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{Name: metricsRuleName},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "metrics-node-compliant"}}
			Expect(k8sClient.Create(ctx, node1)).To(Succeed())
			node1.Status = corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.0"}}
			Expect(k8sClient.Status().Update(ctx, node1)).To(Succeed())

			node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "metrics-node-non-compliant"}}
			Expect(k8sClient.Create(ctx, node2)).To(Succeed())
			node2.Status = corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.15.0-outdated"}}
			Expect(k8sClient.Status().Update(ctx, node2)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			initEvaluated := testutil.ToFloat64(NodesEvaluatedTotal.WithLabelValues(metricsRuleName))
			initCompliant := testutil.ToFloat64(NodesCompliantTotal.WithLabelValues(metricsRuleName))
			initNonCompliant := testutil.ToFloat64(NodesNonCompliantTotal.WithLabelValues(metricsRuleName))

			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "metrics-node-compliant"}})
			Expect(err).NotTo(HaveOccurred())
			_, err = nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "metrics-node-non-compliant"}})
			Expect(err).NotTo(HaveOccurred())

			Expect(testutil.ToFloat64(NodesEvaluatedTotal.WithLabelValues(metricsRuleName))).To(Equal(initEvaluated + 2))
			Expect(testutil.ToFloat64(NodesCompliantTotal.WithLabelValues(metricsRuleName))).To(Equal(initCompliant + 1))
			Expect(testutil.ToFloat64(NodesNonCompliantTotal.WithLabelValues(metricsRuleName))).To(Equal(initNonCompliant + 1))
		})

		It("should update NodeQuarantineStatus gauge based on quarantine status", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{Name: metricsRuleName},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "metrics-quarantine-node"}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			node.Status = corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.15.0-outdated"}}
			Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "metrics-quarantine-node"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(testutil.ToFloat64(NodeQuarantineStatus.WithLabelValues("metrics-quarantine-node", metricsRuleName))).To(Equal(float64(1)))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "metrics-quarantine-node"}, node)).To(Succeed())
			node.Status.NodeInfo.KubeletVersion = "v1.34.0"
			Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

			_, err = nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "metrics-quarantine-node"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(testutil.ToFloat64(NodeQuarantineStatus.WithLabelValues("metrics-quarantine-node", metricsRuleName))).To(Equal(float64(0)))
		})

		It("should increment ReclamationJobsTriggeredTotal and ReclamationJobFailuresTotal counters correctly", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{Name: metricsRuleName},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
					Reclamation: &auditv1alpha1.ReclamationSpec{
						DiskPressure: &auditv1alpha1.DiskPressureReclamation{
							CleanImages: utils.BoolPtr(true),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "metrics-reclaim-node"}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			node.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.0"},
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
				},
			}
			Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

			nodeReconciler := &NodeReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			initTriggered := testutil.ToFloat64(ReclamationJobsTriggeredTotal.WithLabelValues("metrics-reclaim-node", "DiskPressure"))
			initFailures := testutil.ToFloat64(ReclamationJobFailuresTotal.WithLabelValues("metrics-reclaim-node"))

			_, err := nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "metrics-reclaim-node"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(testutil.ToFloat64(ReclamationJobsTriggeredTotal.WithLabelValues("metrics-reclaim-node", "DiskPressure"))).To(Equal(initTriggered + 1))

			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "reclaim-node-metrics-rule-metrics-reclaim-node", Namespace: "default"}, job)).To(Succeed())

			job.Status.Failed = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			_, err = nodeReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "metrics-reclaim-node"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(testutil.ToFloat64(ReclamationJobFailuresTotal.WithLabelValues("metrics-reclaim-node"))).To(Equal(initFailures + 1))
		})
	})
})

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
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
)

var _ = Describe("ClusterRule Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			// Ensure cleanup is done before each test to prevent pollution
			var nodes corev1.NodeList
			Expect(k8sClient.List(ctx, &nodes)).To(Succeed())
			for _, node := range nodes.Items {
				_ = k8sClient.Delete(ctx, &node)
			}
		})

		AfterEach(func() {
			// Cleanup the ClusterRule
			resource := &auditv1alpha1.ClusterRule{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			// Clean up nodes
			var nodes corev1.NodeList
			Expect(k8sClient.List(ctx, &nodes)).To(Succeed())
			for _, node := range nodes.Items {
				_ = k8sClient.Delete(ctx, &node)
			}
		})

		It("should report compliant if no nodes exist (unconditional)", func() {
			// Create ClusterRule with no node selector
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			controllerReconciler := &ClusterRuleReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Fetch rule again
			Expect(k8sClient.Get(ctx, typeNamespacedName, rule)).To(Succeed())
			cond := meta.FindStatusCondition(rule.Status.Conditions, "Compliant")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("AllNodesCompliant"))
		})

		It("should report NoMatchingNodes if nodeSelector is set but no nodes match", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"role": "worker"},
					},
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Create a node that does NOT match the selector
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "master-node",
					Labels: map[string]string{"role": "master"},
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			controllerReconciler := &ClusterRuleReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, rule)).To(Succeed())
			cond := meta.FindStatusCondition(rule.Status.Conditions, "Compliant")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("NoMatchingNodes"))
			Expect(cond.Message).To(Equal("No nodes matched the specified node selector"))
		})

		It("should audit and validate rich metadata for matching nodes", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"role": "worker"},
					},
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion:   "v1.34.0",
						ContainerRuntime: "containerd://1.7.29",
						KernelVersion:    "5.15.0-101-generic",
						OSImage:          "Ubuntu 22.04 LTS",
						Architecture:     "amd64",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Create a compliant worker node
			compliantWorker := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "worker-node-1",
					Labels: map[string]string{"role": "worker"},
				},
			}
			Expect(k8sClient.Create(ctx, compliantWorker)).To(Succeed())
			compliantWorker.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion:          "v1.34.0",
					ContainerRuntimeVersion: "containerd://1.7.29",
					KernelVersion:           "5.15.0-101-generic",
					OSImage:                 "Ubuntu 22.04 LTS",
					Architecture:            "amd64",
				},
			}
			Expect(k8sClient.Status().Update(ctx, compliantWorker)).To(Succeed())

			// Create a non-compliant worker node (mismatching kernel version)
			nonCompliantWorker := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "worker-node-2",
					Labels: map[string]string{"role": "worker"},
				},
			}
			Expect(k8sClient.Create(ctx, nonCompliantWorker)).To(Succeed())
			nonCompliantWorker.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion:          "v1.34.0",
					ContainerRuntimeVersion: "containerd://1.7.29",
					KernelVersion:           "4.19.0-outdated",
					OSImage:                 "Ubuntu 22.04 LTS",
					Architecture:            "amd64",
				},
			}
			Expect(k8sClient.Status().Update(ctx, nonCompliantWorker)).To(Succeed())

			// Create a non-compliant master node that does NOT match selector (should be ignored!)
			nonCompliantMaster := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "master-node",
					Labels: map[string]string{"role": "master"},
				},
			}
			Expect(k8sClient.Create(ctx, nonCompliantMaster)).To(Succeed())
			nonCompliantMaster.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion:          "v1.15.0-extremely-outdated",
					ContainerRuntimeVersion: "docker://1.13.1",
					KernelVersion:           "3.10.0-broken",
					OSImage:                 "CentOS Linux 7",
					Architecture:            "amd64",
				},
			}
			Expect(k8sClient.Status().Update(ctx, nonCompliantMaster)).To(Succeed())

			controllerReconciler := &ClusterRuleReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Fetch the rule and check status
			Expect(k8sClient.Get(ctx, typeNamespacedName, rule)).To(Succeed())
			cond := meta.FindStatusCondition(rule.Status.Conditions, "Compliant")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("NodesNonCompliant"))
			// Only 1 node (worker-node-2) should be reported non-compliant. master-node is ignored.
			Expect(cond.Message).To(Equal("1 node(s) are non-compliant"))
		})

		It("should audit and validate health conditions and minimum resources", func() {
			cpuQty := resource.MustParse("4")
			memQty := resource.MustParse("16Gi")

			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					ExpectedConditions: []auditv1alpha1.NodeConditionRequirement{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
						{
							Type:   corev1.NodeMemoryPressure,
							Status: corev1.ConditionFalse,
						},
					},
					MinimumResources: &auditv1alpha1.MinResourceRequirements{
						CPU:    &cpuQty,
						Memory: &memQty,
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Node 1: Fully compliant node
			compliantNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "compliant-node",
				},
			}
			Expect(k8sClient.Create(ctx, compliantNode)).To(Succeed())
			compliantNode.Status = corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
					{
						Type:   corev1.NodeMemoryPressure,
						Status: corev1.ConditionFalse,
					},
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("32Gi"),
				},
			}
			Expect(k8sClient.Status().Update(ctx, compliantNode)).To(Succeed())

			// Node 2: Non-compliant node due to MemoryPressure condition mismatch
			pressureNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pressure-node",
				},
			}
			Expect(k8sClient.Create(ctx, pressureNode)).To(Succeed())
			pressureNode.Status = corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
					{
						Type:   corev1.NodeMemoryPressure,
						Status: corev1.ConditionTrue, // Mismatch!
					},
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("32Gi"),
				},
			}
			Expect(k8sClient.Status().Update(ctx, pressureNode)).To(Succeed())

			// Node 3: Non-compliant node due to insufficient CPU
			lowCpuNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "low-cpu-node",
				},
			}
			Expect(k8sClient.Create(ctx, lowCpuNode)).To(Succeed())
			lowCpuNode.Status = corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
					{
						Type:   corev1.NodeMemoryPressure,
						Status: corev1.ConditionFalse,
					},
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"), // Mismatch!
					corev1.ResourceMemory: resource.MustParse("32Gi"),
				},
			}
			Expect(k8sClient.Status().Update(ctx, lowCpuNode)).To(Succeed())

			controllerReconciler := &ClusterRuleReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Fetch the rule and check status
			Expect(k8sClient.Get(ctx, typeNamespacedName, rule)).To(Succeed())
			cond := meta.FindStatusCondition(rule.Status.Conditions, "Compliant")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("NodesNonCompliant"))
			// 2 nodes (pressure-node, low-cpu-node) should be non-compliant
			Expect(cond.Message).To(Equal("2 node(s) are non-compliant"))
		})
	})
})

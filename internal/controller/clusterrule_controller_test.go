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

		It("should actively add and remove compliance labels and custom labels on matching nodes", func() {
			labelKey := "policy.kubeorthos.io/compliant"
			labelVal := "true"
			customKey := "environment"
			customVal := "production"

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

			// Node 1: Compliant node that is missing the labels (should be applied)
			compliantNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "compliant-node-label",
				},
			}
			Expect(k8sClient.Create(ctx, compliantNode)).To(Succeed())
			compliantNode.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.34.0",
				},
			}
			Expect(k8sClient.Status().Update(ctx, compliantNode)).To(Succeed())

			// Node 2: Non-compliant node that has the labels (should be removed)
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
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.15.0-outdated",
				},
			}
			Expect(k8sClient.Status().Update(ctx, nonCompliantNode)).To(Succeed())

			controllerReconciler := &ClusterRuleReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify Node 1 now carries the compliance and custom labels
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "compliant-node-label"}, compliantNode)).To(Succeed())
			Expect(compliantNode.Labels).To(HaveKeyWithValue(labelKey, labelVal))
			Expect(compliantNode.Labels).To(HaveKeyWithValue(customKey, customVal))

			// Verify Node 2 has had the compliance and custom labels removed
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "non-compliant-node-label"}, nonCompliantNode)).To(Succeed())
			Expect(nonCompliantNode.Labels).NotTo(HaveKey(labelKey))
			Expect(nonCompliantNode.Labels).NotTo(HaveKey(customKey))
		})

		It("should cordon non-compliant worker nodes, skip control-plane nodes, and uncordon when compliant under ActionEnforce", func() {
			enforceRuleName := "test-enforce-rule"
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      enforceRuleName,
					Namespace: "default",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Node 1: Non-compliant worker node (should be cordoned and annotated)
			workerNonCompliant := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-non-compliant",
				},
			}
			Expect(k8sClient.Create(ctx, workerNonCompliant)).To(Succeed())
			workerNonCompliant.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.15.0-outdated",
				},
			}
			Expect(k8sClient.Status().Update(ctx, workerNonCompliant)).To(Succeed())

			// Node 2: Non-compliant control plane node (should NOT be cordoned)
			cpNonCompliant := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cp-non-compliant",
					Labels: map[string]string{
						"node-role.kubernetes.io/control-plane": "",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cpNonCompliant)).To(Succeed())
			cpNonCompliant.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.15.0-outdated",
				},
			}
			Expect(k8sClient.Status().Update(ctx, cpNonCompliant)).To(Succeed())

			// Node 3: Compliant worker node that was previously quarantined (should be uncordoned and annotation removed)
			workerCompliantQuarantined := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-compliant-quarantined",
					Annotations: map[string]string{
						"policy.kubeorthos.io/quarantined": "true",
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
			}
			Expect(k8sClient.Create(ctx, workerCompliantQuarantined)).To(Succeed())
			workerCompliantQuarantined.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.34.0",
				},
			}
			Expect(k8sClient.Status().Update(ctx, workerCompliantQuarantined)).To(Succeed())

			controllerReconciler := &ClusterRuleReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: enforceRuleName, Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify Node 1: Non-compliant worker is now cordoned and has the quarantine annotation
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-non-compliant"}, workerNonCompliant)).To(Succeed())
			Expect(workerNonCompliant.Spec.Unschedulable).To(BeTrue())
			Expect(workerNonCompliant.Annotations).To(HaveKeyWithValue("policy.kubeorthos.io/quarantined", "true"))

			// Verify Node 2: Non-compliant control plane node is NOT cordoned
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cp-non-compliant"}, cpNonCompliant)).To(Succeed())
			Expect(cpNonCompliant.Spec.Unschedulable).To(BeFalse())

			// Verify Node 3: Compliant worker node has been uncordoned and quarantine annotation removed
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-compliant-quarantined"}, workerCompliantQuarantined)).To(Succeed())
			Expect(workerCompliantQuarantined.Spec.Unschedulable).To(BeFalse())
			Expect(workerCompliantQuarantined.Annotations).NotTo(HaveKey("policy.kubeorthos.io/quarantined"))
		})

		It("should uncordon worker nodes when a rule action transitions from Enforce to Audit", func() {
			transitionRuleName := "test-transition-rule"

			// 1. Start with ActionEnforce
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      transitionRuleName,
					Namespace: "default",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Previously quarantined worker node
			quarantinedWorker := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "quarantined-worker-transition",
					Annotations: map[string]string{
						"policy.kubeorthos.io/quarantined": "true",
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
			}
			Expect(k8sClient.Create(ctx, quarantinedWorker)).To(Succeed())
			quarantinedWorker.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.15.0-outdated", // still non-compliant
				},
			}
			Expect(k8sClient.Status().Update(ctx, quarantinedWorker)).To(Succeed())

			controllerReconciler := &ClusterRuleReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			// Reconcile once in Enforce: Node should remain quarantined since it's still non-compliant
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: transitionRuleName, Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "quarantined-worker-transition"}, quarantinedWorker)).To(Succeed())
			Expect(quarantinedWorker.Spec.Unschedulable).To(BeTrue())
			Expect(quarantinedWorker.Annotations).To(HaveKeyWithValue("policy.kubeorthos.io/quarantined", "true"))

			// 2. Change rule Action to Audit
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: transitionRuleName, Namespace: "default"}, rule)).To(Succeed())
			rule.Spec.Action = auditv1alpha1.ActionAudit
			Expect(k8sClient.Update(ctx, rule)).To(Succeed())

			// Reconcile again: The node should now be actively uncordoned and quarantine annotation removed
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: transitionRuleName, Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "quarantined-worker-transition"}, quarantinedWorker)).To(Succeed())
			Expect(quarantinedWorker.Spec.Unschedulable).To(BeFalse())
			Expect(quarantinedWorker.Annotations).NotTo(HaveKey("policy.kubeorthos.io/quarantined"))
		})
	})
})

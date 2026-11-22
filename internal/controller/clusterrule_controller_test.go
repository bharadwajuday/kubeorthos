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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
			// Cleanup all ClusterRules (stripping finalizers if still present)
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
			cond := meta.FindStatusCondition(rule.Status.Conditions, constants.ConditionTypeCompliant)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal(constants.ConditionReasonAllNodesCompliant))
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
			cond := meta.FindStatusCondition(rule.Status.Conditions, constants.ConditionTypeCompliant)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal(constants.ConditionReasonNoMatchingNodes))
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
			cond := meta.FindStatusCondition(rule.Status.Conditions, constants.ConditionTypeCompliant)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(constants.ConditionReasonNodesNonCompliant))
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
			cond := meta.FindStatusCondition(rule.Status.Conditions, constants.ConditionTypeCompliant)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(constants.ConditionReasonNodesNonCompliant))
			// 2 nodes (pressure-node, low-cpu-node) should be non-compliant
			Expect(cond.Message).To(Equal("2 node(s) are non-compliant"))
		})

		It("should accurately populate status summary and nonCompliantNodes details without mutating nodes or creating jobs", func() {
			// Create 3 nodes:
			// - worker-compliant: compliant
			// - worker-mismatch: non-compliant (kubelet version mismatch, already quarantined by NodeReconciler, experiencing DiskPressure)
			// - cp-node: control plane node (excluded from total and matched)
			node1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "worker-compliant",
					Labels: map[string]string{"kubernetes.io/os": "linux", "tier": "backend"},
				},
			}
			Expect(k8sClient.Create(ctx, node1)).To(Succeed())
			node1.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.34.0",
				},
			}
			Expect(k8sClient.Status().Update(ctx, node1)).To(Succeed())

			node2 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "worker-mismatch",
					Labels: map[string]string{"kubernetes.io/os": "linux", "tier": "backend"},
					Annotations: map[string]string{
						constants.AnnotationQuarantinePrefix + resourceName: constants.ValueTrue,
					},
				},
			}
			Expect(k8sClient.Create(ctx, node2)).To(Succeed())
			node2.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.32.0",
				},
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
				},
			}
			Expect(k8sClient.Status().Update(ctx, node2)).To(Succeed())

			cpNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "cp-node",
					Labels: map[string]string{constants.LabelNodeRoleControlPlane: ""},
				},
			}
			Expect(k8sClient.Create(ctx, cpNode)).To(Succeed())
			cpNode.Status = corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.32.0",
				},
			}
			Expect(k8sClient.Status().Update(ctx, cpNode)).To(Succeed())

			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"tier": "backend"},
					},
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
					ComplianceLabel: &auditv1alpha1.ComplianceLabelSpec{
						Key:   "policy.kubeorthos.io/compliant",
						Value: "true",
					},
					Reclamation: &auditv1alpha1.ReclamationSpec{
						DiskPressure: &auditv1alpha1.DiskPressureReclamation{
							CleanImages: utils.BoolPtr(true),
						},
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

			// Fetch updated rule
			updatedRule := &auditv1alpha1.ClusterRule{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updatedRule)).To(Succeed())

			// Verify Summary counts
			Expect(updatedRule.Status.Summary).NotTo(BeNil())
			Expect(updatedRule.Status.Summary.TotalNodes).To(Equal(2))
			Expect(updatedRule.Status.Summary.MatchedNodes).To(Equal(2))
			Expect(updatedRule.Status.Summary.CompliantNodes).To(Equal(1))
			Expect(updatedRule.Status.Summary.NonCompliantNodes).To(Equal(1))

			// Verify NonCompliantNodes details
			Expect(updatedRule.Status.NonCompliantNodes).To(HaveLen(1))
			detail := updatedRule.Status.NonCompliantNodes[0]
			Expect(detail.Name).To(Equal("worker-mismatch"))
			Expect(detail.Quarantined).To(BeTrue())
			Expect(detail.Reasons).To(ContainElement(ContainSubstring("KubeletVersion mismatch")))

			// Verify ClusterRuleReconciler did NOT mutate nodes or create reclamation Jobs
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-compliant"}, node1)).To(Succeed())
			Expect(node1.Labels).NotTo(HaveKey("policy.kubeorthos.io/compliant"))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-mismatch"}, node2)).To(Succeed())
			Expect(node2.Spec.Unschedulable).To(BeFalse())

			var jobs batchv1.JobList
			Expect(k8sClient.List(ctx, &jobs)).To(Succeed())
			Expect(jobs.Items).To(BeEmpty())
		})

		It("should add finalizer on reconcile and clean up node labels and quarantine claims upon deletion", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: resourceName,
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
					ComplianceLabel: &auditv1alpha1.ComplianceLabelSpec{
						Key:   "policy.kubeorthos.io/compliant",
						Value: "true",
					},
					CustomLabels: map[string]string{
						"environment": "production",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			worker := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-finalizer-cleanup",
					Labels: map[string]string{
						constants.LabelCompliancePrefix + resourceName: "false",
						"policy.kubeorthos.io/compliant":               "true",
						"environment":                                  "production",
					},
					Annotations: map[string]string{
						constants.AnnotationQuarantinePrefix + resourceName: constants.ValueTrue,
						constants.AnnotationQuarantined:                     constants.ValueTrue,
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
			}
			Expect(k8sClient.Create(ctx, worker)).To(Succeed())

			controllerReconciler := &ClusterRuleReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			// 1. First reconcile registers the finalizer
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, rule)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(rule, constants.ClusterRuleFinalizer)).To(BeTrue())

			// 2. Delete the ClusterRule -> DeletionTimestamp is set, object still exists due to finalizer
			Expect(k8sClient.Delete(ctx, rule)).To(Succeed())
			Expect(k8sClient.Get(ctx, typeNamespacedName, rule)).To(Succeed())
			Expect(rule.DeletionTimestamp.IsZero()).To(BeFalse())

			// 3. Reconcile deletion -> cleans up worker node and removes finalizer
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify ClusterRule is completely deleted
			err = k8sClient.Get(ctx, typeNamespacedName, rule)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			// Verify worker node was cleaned up and uncordoned
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-finalizer-cleanup"}, worker)).To(Succeed())
			Expect(worker.Labels).NotTo(HaveKey(constants.LabelCompliancePrefix + resourceName))
			Expect(worker.Labels).NotTo(HaveKey("policy.kubeorthos.io/compliant"))
			Expect(worker.Labels).NotTo(HaveKey("environment"))
			Expect(worker.Annotations).NotTo(HaveKey(constants.AnnotationQuarantinePrefix + resourceName))
			Expect(worker.Annotations).NotTo(HaveKey(constants.AnnotationQuarantined))
			Expect(worker.Spec.Unschedulable).To(BeFalse())
		})

		It("should keep node cordoned upon ClusterRule deletion if another rule still holds a quarantine claim", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: resourceName,
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionEnforce,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.34.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			worker := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-shared-finalizer",
					Labels: map[string]string{
						constants.LabelCompliancePrefix + resourceName: "false",
						constants.LabelCompliancePrefix + "other-rule": "false",
					},
					Annotations: map[string]string{
						constants.AnnotationQuarantinePrefix + resourceName: constants.ValueTrue,
						constants.AnnotationQuarantinePrefix + "other-rule": constants.ValueTrue,
						constants.AnnotationQuarantined:                     constants.ValueTrue,
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
			}
			Expect(k8sClient.Create(ctx, worker)).To(Succeed())

			controllerReconciler := &ClusterRuleReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(100),
			}

			// Register finalizer, then delete and reconcile
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, rule)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify only this rule's label & quarantine claim were removed; node stays cordoned for other-rule
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-shared-finalizer"}, worker)).To(Succeed())
			Expect(worker.Labels).NotTo(HaveKey(constants.LabelCompliancePrefix + resourceName))
			Expect(worker.Labels).To(HaveKeyWithValue(constants.LabelCompliancePrefix+"other-rule", "false"))
			Expect(worker.Annotations).NotTo(HaveKey(constants.AnnotationQuarantinePrefix + resourceName))
			Expect(worker.Annotations).To(HaveKeyWithValue(constants.AnnotationQuarantinePrefix+"other-rule", constants.ValueTrue))
			Expect(worker.Annotations).To(HaveKeyWithValue(constants.AnnotationQuarantined, constants.ValueTrue))
			Expect(worker.Spec.Unschedulable).To(BeTrue())
		})
	})

	Describe("Rule Watch Node Event Predicate Filter", func() {
		var pred ruleWatchNodePredicate

		BeforeEach(func() {
			pred = ruleWatchNodePredicate{}
		})

		It("should approve Create and Delete events only if they carry compliance or quarantine labels/annotations", func() {
			// Without compliance/quarantine labels/annotations
			nodeEmpty := &corev1.Node{}
			Expect(pred.Create(event.CreateEvent{Object: nodeEmpty})).To(BeFalse())
			Expect(pred.Delete(event.DeleteEvent{Object: nodeEmpty})).To(BeFalse())

			// With compliance label
			nodeCompliance := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"compliance.kubeorthos.io/rule-a": "true",
					},
				},
			}
			Expect(pred.Create(event.CreateEvent{Object: nodeCompliance})).To(BeTrue())
			Expect(pred.Delete(event.DeleteEvent{Object: nodeCompliance})).To(BeTrue())

			// With quarantine annotation
			nodeQuarantine := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"quarantine.kubeorthos.io/rule-b": "true",
					},
				},
			}
			Expect(pred.Create(event.CreateEvent{Object: nodeQuarantine})).To(BeTrue())
			Expect(pred.Delete(event.DeleteEvent{Object: nodeQuarantine})).To(BeTrue())
		})

		It("should ignore standard Node heartbeat updates and other unrelated changes", func() {
			oldNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"role": "worker",
					},
				},
				Spec: corev1.NodeSpec{
					Unschedulable: false,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionTrue,
							LastHeartbeatTime:  metav1.Unix(100, 0),
							LastTransitionTime: metav1.Unix(100, 0),
						},
					},
					NodeInfo: corev1.NodeSystemInfo{
						KubeletVersion: "v1.34.0",
					},
				},
			}

			// Heartbeat change
			newNode := oldNode.DeepCopy()
			newNode.Status.Conditions[0].LastHeartbeatTime = metav1.Unix(200, 0)
			newNode.Status.Conditions[0].LastTransitionTime = metav1.Unix(200, 0)

			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: newNode})).To(BeFalse())

			// Cordoning change
			newNode2 := oldNode.DeepCopy()
			newNode2.Spec.Unschedulable = true
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: newNode2})).To(BeFalse())

			// KubeletVersion change
			newNode3 := oldNode.DeepCopy()
			newNode3.Status.NodeInfo.KubeletVersion = "v1.35.0"
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: newNode3})).To(BeFalse())

			// Unrelated label change
			newNode4 := oldNode.DeepCopy()
			newNode4.Labels["role"] = "master"
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: newNode4})).To(BeFalse())
		})

		It("should trigger on compliance or quarantine label/annotation changes", func() {
			oldNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"role": "worker",
					},
				},
			}

			// Add compliance label
			newNode1 := oldNode.DeepCopy()
			newNode1.Labels["compliance.kubeorthos.io/rule-a"] = "true"
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: newNode1})).To(BeTrue())

			// Modify compliance label value
			newNode2 := newNode1.DeepCopy()
			newNode2.Labels["compliance.kubeorthos.io/rule-a"] = "false"
			Expect(pred.Update(event.UpdateEvent{ObjectOld: newNode1, ObjectNew: newNode2})).To(BeTrue())

			// Delete compliance label
			Expect(pred.Update(event.UpdateEvent{ObjectOld: newNode1, ObjectNew: oldNode})).To(BeTrue())

			// Add quarantine annotation
			newNode3 := oldNode.DeepCopy()
			newNode3.Annotations = map[string]string{
				"quarantine.kubeorthos.io/rule-b": "true",
			}
			Expect(pred.Update(event.UpdateEvent{ObjectOld: oldNode, ObjectNew: newNode3})).To(BeTrue())
		})
	})
})

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
	"fmt"

	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
	"kubeorthos/internal/constants"
	"kubeorthos/internal/utils"

	batchv1 "k8s.io/api/batch/v1"
)

// ClusterRuleReconciler reconciles a ClusterRule object
type ClusterRuleReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=audit.kubeorthos.io,resources=clusterrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=audit.kubeorthos.io,resources=clusterrules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=audit.kubeorthos.io,resources=clusterrules/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ClusterRule object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/reconcile
func (r *ClusterRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the ClusterRule instance
	var rule auditv1alpha1.ClusterRule
	if err := r.Get(ctx, req.NamespacedName, &rule); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch ClusterRule")
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ClusterRule", "name", rule.Name, "action", rule.Spec.Action)

	// List Nodes matching the selector
	var nodeList corev1.NodeList
	var listOpts []client.ListOption

	if rule.Spec.NodeSelector != nil {
		var selector labels.Selector
		var err error
		selector, err = metav1.LabelSelectorAsSelector(rule.Spec.NodeSelector)
		if err != nil {
			log.Error(err, "unable to parse NodeSelector")
			return ctrl.Result{}, err
		}
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: selector})
	}

	if err := r.List(ctx, &nodeList, listOpts...); err != nil {
		log.Error(err, "unable to list Nodes")
		return ctrl.Result{}, err
	}

	nonCompliantNodes := []string{}
	for _, node := range nodeList.Items {
		isCompliant, mismatchMsg := r.auditNodeCompliance(&rule, &node)

		log.Info("Audited node against ClusterRule expectations", "node", node.Name, "kubeletVersionChecked", rule.Spec.ExpectedNodeConfig.KubeletVersion != "", "containerRuntimeChecked", rule.Spec.ExpectedNodeConfig.ContainerRuntime != "", "kernelVersionChecked", rule.Spec.ExpectedNodeConfig.KernelVersion != "", "osImageChecked", rule.Spec.ExpectedNodeConfig.OSImage != "", "architectureChecked", rule.Spec.ExpectedNodeConfig.Architecture != "", "conditionsCheckedCount", len(rule.Spec.ExpectedConditions), "minimumResourcesChecked", rule.Spec.MinimumResources != nil, "isCompliant", isCompliant)

		if !isCompliant {
			nonCompliantNodes = append(nonCompliantNodes, node.Name)
			log.Info("Node is non-compliant", "node", node.Name, "mismatches", mismatchMsg)
			switch rule.Spec.Action {
			case auditv1alpha1.ActionAudit:
				r.Recorder.Eventf(&rule, nil, corev1.EventTypeWarning, constants.EventReasonNonCompliantNode, constants.EventActionAudit, "Node %s is non-compliant: %s", node.Name, mismatchMsg)
			case auditv1alpha1.ActionEnforce:
				r.Recorder.Eventf(&rule, nil, corev1.EventTypeWarning, constants.EventReasonNonCompliantNodeEnforced, constants.EventActionEnforce, "Node %s is non-compliant and is being enforced: %s", node.Name, mismatchMsg)
			}
		}

		cordonUpdated := r.reconcileCordoning(&rule, &node, isCompliant)
		labelUpdated := r.reconcileLabeling(&rule, &node, isCompliant)

		if cordonUpdated || labelUpdated {
			if err := r.Update(ctx, &node); err != nil {
				log.Error(err, "unable to update node specs/labels/annotations", "node", node.Name)
				return ctrl.Result{}, err
			}
			log.Info("Successfully updated node state", "node", node.Name)
		}

		// Automated Resource Reclamation
		if err := r.reconcileReclamation(ctx, &rule, &node); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Update Status
	condition := metav1.Condition{
		Type:               constants.ConditionTypeCompliant,
		Status:             metav1.ConditionTrue,
		Reason:             constants.ConditionReasonAllNodesCompliant,
		Message:            "All nodes match the expected configuration",
		ObservedGeneration: rule.Generation,
	}

	if rule.Spec.NodeSelector != nil && len(nodeList.Items) == 0 {
		condition.Reason = constants.ConditionReasonNoMatchingNodes
		condition.Message = "No nodes matched the specified node selector"
	} else if len(nonCompliantNodes) > 0 {
		condition.Status = metav1.ConditionFalse
		condition.Reason = constants.ConditionReasonNodesNonCompliant
		condition.Message = fmt.Sprintf("%d node(s) are non-compliant", len(nonCompliantNodes))
	}

	meta.SetStatusCondition(&rule.Status.Conditions, condition)
	if err := r.Status().Update(ctx, &rule); err != nil {
		log.Error(err, "unable to update ClusterRule status")
		return ctrl.Result{}, err
	}

	log.Info("Reconciled ClusterRule successfully", "name", rule.Name, "compliant", condition.Status == metav1.ConditionTrue, "reason", condition.Reason)

	return ctrl.Result{}, nil
}

// auditNodeCompliance checks if a node complies with the expectations of the ClusterRule
func (r *ClusterRuleReconciler) auditNodeCompliance(rule *auditv1alpha1.ClusterRule, node *corev1.Node) (bool, string) {
	isCompliant := true
	var mismatchMsg strings.Builder

	// Check KubeletVersion
	if rule.Spec.ExpectedNodeConfig.KubeletVersion != "" && node.Status.NodeInfo.KubeletVersion != rule.Spec.ExpectedNodeConfig.KubeletVersion {
		isCompliant = false
		mismatchMsg.WriteString(fmt.Sprintf("KubeletVersion mismatch: expected %s, got %s. ", rule.Spec.ExpectedNodeConfig.KubeletVersion, node.Status.NodeInfo.KubeletVersion))
	}

	// Check ContainerRuntime
	if rule.Spec.ExpectedNodeConfig.ContainerRuntime != "" && node.Status.NodeInfo.ContainerRuntimeVersion != rule.Spec.ExpectedNodeConfig.ContainerRuntime {
		isCompliant = false
		mismatchMsg.WriteString(fmt.Sprintf("ContainerRuntime mismatch: expected %s, got %s. ", rule.Spec.ExpectedNodeConfig.ContainerRuntime, node.Status.NodeInfo.ContainerRuntimeVersion))
	}

	// Check KernelVersion
	if rule.Spec.ExpectedNodeConfig.KernelVersion != "" && node.Status.NodeInfo.KernelVersion != rule.Spec.ExpectedNodeConfig.KernelVersion {
		isCompliant = false
		mismatchMsg.WriteString(fmt.Sprintf("KernelVersion mismatch: expected %s, got %s. ", rule.Spec.ExpectedNodeConfig.KernelVersion, node.Status.NodeInfo.KernelVersion))
	}

	// Check OSImage
	if rule.Spec.ExpectedNodeConfig.OSImage != "" && node.Status.NodeInfo.OSImage != rule.Spec.ExpectedNodeConfig.OSImage {
		isCompliant = false
		mismatchMsg.WriteString(fmt.Sprintf("OSImage mismatch: expected %s, got %s. ", rule.Spec.ExpectedNodeConfig.OSImage, node.Status.NodeInfo.OSImage))
	}

	// Check Architecture
	if rule.Spec.ExpectedNodeConfig.Architecture != "" && node.Status.NodeInfo.Architecture != rule.Spec.ExpectedNodeConfig.Architecture {
		isCompliant = false
		mismatchMsg.WriteString(fmt.Sprintf("Architecture mismatch: expected %s, got %s. ", rule.Spec.ExpectedNodeConfig.Architecture, node.Status.NodeInfo.Architecture))
	}

	// Check ExpectedConditions
	for _, reqCond := range rule.Spec.ExpectedConditions {
		var found bool
		for _, nodeCond := range node.Status.Conditions {
			if nodeCond.Type == reqCond.Type {
				found = true
				if nodeCond.Status != reqCond.Status {
					isCompliant = false
					mismatchMsg.WriteString(fmt.Sprintf("Node Condition %s is %s, expected %s. ", reqCond.Type, nodeCond.Status, reqCond.Status))
				}
				break
			}
		}
		if !found {
			isCompliant = false
			mismatchMsg.WriteString(fmt.Sprintf("Node Condition %s is missing, expected %s. ", reqCond.Type, reqCond.Status))
		}
	}

	// Check MinimumResources
	if rule.Spec.MinimumResources != nil {
		if rule.Spec.MinimumResources.CPU != nil {
			actualCPU := node.Status.Allocatable[corev1.ResourceCPU]
			if actualCPU.Cmp(*rule.Spec.MinimumResources.CPU) < 0 {
				isCompliant = false
				mismatchMsg.WriteString(fmt.Sprintf("Allocatable CPU is insufficient: expected %s, got %s. ", rule.Spec.MinimumResources.CPU.String(), actualCPU.String()))
			}
		}
		if rule.Spec.MinimumResources.Memory != nil {
			actualMemory := node.Status.Allocatable[corev1.ResourceMemory]
			if actualMemory.Cmp(*rule.Spec.MinimumResources.Memory) < 0 {
				isCompliant = false
				mismatchMsg.WriteString(fmt.Sprintf("Allocatable Memory is insufficient: expected %s, got %s. ", rule.Spec.MinimumResources.Memory.String(), actualMemory.String()))
			}
		}
		if rule.Spec.MinimumResources.Storage != nil {
			actualStorage := node.Status.Allocatable[corev1.ResourceEphemeralStorage]
			if actualStorage.Cmp(*rule.Spec.MinimumResources.Storage) < 0 {
				isCompliant = false
				mismatchMsg.WriteString(fmt.Sprintf("Allocatable Ephemeral Storage is insufficient: expected %s, got %s. ", rule.Spec.MinimumResources.Storage.String(), actualStorage.String()))
			}
		}
	}

	return isCompliant, mismatchMsg.String()
}

// reconcileCordoning handles cordoning and uncordoning based on rule action and node compliance
func (r *ClusterRuleReconciler) reconcileCordoning(rule *auditv1alpha1.ClusterRule, node *corev1.Node, isCompliant bool) bool {
	nodeUpdated := false

	if rule.Spec.Action == auditv1alpha1.ActionEnforce {
		// Check if control plane node
		_, isCP := node.Labels[constants.LabelNodeRoleControlPlane]
		_, isMaster := node.Labels[constants.LabelNodeRoleMaster]
		isControlPlane := isCP || isMaster

		if isControlPlane {
			return false
		}

		if !isCompliant {
			if !node.Spec.Unschedulable {
				node.Spec.Unschedulable = true
				if node.Annotations == nil {
					node.Annotations = make(map[string]string)
				}
				node.Annotations[constants.AnnotationQuarantined] = "true"
				nodeUpdated = true
				r.Recorder.Eventf(rule, nil, corev1.EventTypeWarning, constants.EventReasonCordonedNode, constants.EventActionEnforcement, "Successfully cordoned non-compliant worker node %s", node.Name)
			}
		} else {
			// Compliant node - check if it was quarantined by KubeOrthos
			if node.Annotations != nil {
				if _, isQuarantined := node.Annotations[constants.AnnotationQuarantined]; isQuarantined {
					node.Spec.Unschedulable = false
					delete(node.Annotations, constants.AnnotationQuarantined)
					nodeUpdated = true
					r.Recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonUncordonedNode, constants.EventActionEnforcement, "Successfully uncordoned compliant worker node %s", node.Name)
				}
			}
		}
	} else {
		// Rule is in Audit mode (or not Enforce) - lift any existing quarantines applied by KubeOrthos
		if node.Annotations != nil {
			if _, isQuarantined := node.Annotations[constants.AnnotationQuarantined]; isQuarantined {
				node.Spec.Unschedulable = false
				delete(node.Annotations, constants.AnnotationQuarantined)
				nodeUpdated = true
				r.Recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonUncordonedNode, constants.EventActionEnforcement, "Transitioned to Audit: Successfully uncordoned worker node %s", node.Name)
			}
		}
	}

	return nodeUpdated
}

// reconcileLabeling applies or removes compliance/custom labels on the node
func (r *ClusterRuleReconciler) reconcileLabeling(rule *auditv1alpha1.ClusterRule, node *corev1.Node, isCompliant bool) bool {
	nodeUpdated := false

	if rule.Spec.ComplianceLabel == nil && len(rule.Spec.CustomLabels) == 0 {
		return false
	}

	nodeLabels := node.GetLabels()
	if nodeLabels == nil {
		nodeLabels = make(map[string]string)
	}

	if isCompliant {
		// Node is compliant, ensure compliance label is applied
		if rule.Spec.ComplianceLabel != nil {
			labelKey := rule.Spec.ComplianceLabel.Key
			labelValue := rule.Spec.ComplianceLabel.Value
			if val, exists := nodeLabels[labelKey]; !exists || val != labelValue {
				nodeLabels[labelKey] = labelValue
				nodeUpdated = true
				r.Recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonComplianceLabelApplied, constants.EventActionComplianceLabel, "Successfully applied label %s=%s to node %s", labelKey, labelValue, node.Name)
			}
		}
		// Node is compliant, ensure custom labels are applied
		for k, v := range rule.Spec.CustomLabels {
			if val, exists := nodeLabels[k]; !exists || val != v {
				nodeLabels[k] = v
				nodeUpdated = true
				r.Recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonCustomLabelApplied, constants.EventActionComplianceLabel, "Successfully applied custom label %s=%s to node %s", k, v, node.Name)
			}
		}
	} else {
		// Node is non-compliant, ensure compliance label is removed
		if rule.Spec.ComplianceLabel != nil {
			labelKey := rule.Spec.ComplianceLabel.Key
			if _, exists := nodeLabels[labelKey]; exists {
				delete(nodeLabels, labelKey)
				nodeUpdated = true
				r.Recorder.Eventf(rule, nil, corev1.EventTypeWarning, constants.EventReasonComplianceLabelRemoved, constants.EventActionComplianceLabel, "Successfully removed label %s from non-compliant node %s", labelKey, node.Name)
			}
		}
		// Node is non-compliant, ensure custom labels are removed
		for k := range rule.Spec.CustomLabels {
			if _, exists := nodeLabels[k]; exists {
				delete(nodeLabels, k)
				nodeUpdated = true
				r.Recorder.Eventf(rule, nil, corev1.EventTypeWarning, constants.EventReasonCustomLabelRemoved, constants.EventActionComplianceLabel, "Successfully removed custom label %s from non-compliant node %s", k, node.Name)
			}
		}
	}

	if nodeUpdated {
		node.SetLabels(nodeLabels)
	}

	return nodeUpdated
}

// reconcileReclamation checks for Node disk pressure and spawns/manages resource reclamation Jobs
func (r *ClusterRuleReconciler) reconcileReclamation(ctx context.Context, rule *auditv1alpha1.ClusterRule, node *corev1.Node) error {
	log := logf.FromContext(ctx)

	if rule.Spec.Reclamation == nil || rule.Spec.Reclamation.DiskPressure == nil {
		return nil
	}

	hasDiskPressure := false
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeDiskPressure && cond.Status == corev1.ConditionTrue {
			hasDiskPressure = true
			break
		}
	}

	if !hasDiskPressure {
		return nil
	}

	jobList := &batchv1.JobList{}
	labelSelector := labels.SelectorFromSet(labels.Set{
		constants.LabelReclamationNode: node.Name,
		constants.LabelClusterRule:     rule.Name,
	})
	if err := r.List(ctx, jobList, client.InNamespace(rule.Namespace), client.MatchingLabelsSelector{Selector: labelSelector}); err != nil {
		log.Error(err, "unable to list reclamation jobs", "node", node.Name)
		return err
	}

	if len(jobList.Items) == 0 {
		safeNodeName := strings.ReplaceAll(node.Name, ".", "-")
		jobName := fmt.Sprintf("reclaim-%s-%s", rule.Name, safeNodeName)
		if len(jobName) > 63 {
			jobName = jobName[:63]
		}

		jobLabels := map[string]string{
			constants.LabelReclamationNode: node.Name,
			constants.LabelClusterRule:     rule.Name,
		}

		cleanImages := true
		if rule.Spec.Reclamation.DiskPressure.CleanImages != nil {
			cleanImages = *rule.Spec.Reclamation.DiskPressure.CleanImages
		}
		cleanContainers := true
		if rule.Spec.Reclamation.DiskPressure.CleanContainers != nil {
			cleanContainers = *rule.Spec.Reclamation.DiskPressure.CleanContainers
		}
		cleanLogs := false
		if rule.Spec.Reclamation.DiskPressure.CleanLogs != nil {
			cleanLogs = *rule.Spec.Reclamation.DiskPressure.CleanLogs
		}
		logSizeLimit := constants.DefaultLogSizeLimit
		if rule.Spec.Reclamation.DiskPressure.LogSizeLimit != "" {
			logSizeLimit = rule.Spec.Reclamation.DiskPressure.LogSizeLimit
		}

		script := "#!/bin/sh\nset -ex\n"
		if cleanImages {
			script += "echo 'Pruning containerd images...'\n"
			script += "crictl rmi --prune || true\n"
		}
		if cleanContainers {
			script += "echo 'Pruning stopped containers...'\n"
			script += "crictl rm $(crictl ps -a -q) || true\n"
		}
		if cleanLogs {
			script += "echo 'Truncating large logs...'\n"
			script += fmt.Sprintf("find /host/var/log/pods -type f -name '*.log' -size +%s -exec truncate -s 0 {} \\;\n", logSizeLimit)
		}

		job := utils.NewReclamationJob(jobName, rule.Namespace, node.Name, script, jobLabels)

		if err := r.Create(ctx, job); err != nil {
			log.Error(err, "unable to create reclamation job", "job", jobName)
			return err
		}
		log.Info("Triggered automated resource reclamation Job", "node", node.Name, "job", jobName)
		r.Recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonReclamationTriggered, constants.EventActionReclamation, "Triggered automated reclamation Job %s on node %s", jobName, node.Name)
	} else {
		// Check active Job status
		existingJob := &jobList.Items[0]
		if existingJob.Status.Succeeded > 0 {
			if err := r.Delete(ctx, existingJob); err != nil {
				log.Error(err, "unable to delete succeeded reclamation job", "job", existingJob.Name)
				return err
			}
			log.Info("Automated resource reclamation completed successfully", "node", node.Name)
			r.Recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonReclamationCompleted, constants.EventActionReclamation, "Automated resource reclamation completed successfully on node %s", node.Name)
		} else if existingJob.Status.Failed > 0 {
			if err := r.Delete(ctx, existingJob); err != nil {
				log.Error(err, "unable to delete failed reclamation job", "job", existingJob.Name)
				return err
			}
			log.Info("Automated resource reclamation failed", "node", node.Name)
			r.Recorder.Eventf(rule, nil, corev1.EventTypeWarning, constants.EventReasonReclamationFailed, constants.EventActionReclamation, "Automated resource reclamation failed on node %s", node.Name)
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterRuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&auditv1alpha1.ClusterRule{}).
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				// We want to trigger reconciliation for all ClusterRules when any Node changes
				var rules auditv1alpha1.ClusterRuleList
				if err := r.List(ctx, &rules); err != nil {
					return nil
				}
				requests := make([]reconcile.Request, len(rules.Items))
				for i, rule := range rules.Items {
					requests[i] = reconcile.Request{
						NamespacedName: client.ObjectKey{
							Name:      rule.Name,
							Namespace: rule.Namespace,
						},
					}
				}
				return requests
			}),
		).
		Named("clusterrule").
		Complete(r)
}

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

	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
	"kubeorthos/internal/constants"
	"kubeorthos/internal/utils"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// NodesEvaluatedTotal tracks total node evaluations
	NodesEvaluatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeorthos_nodes_evaluated_total",
			Help: "Total number of nodes evaluated against a ClusterRule.",
		},
		[]string{"rule_name"},
	)

	// NodesCompliantTotal tracks total compliant nodes
	NodesCompliantTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeorthos_nodes_compliant_total",
			Help: "Total number of nodes evaluated as compliant against a ClusterRule.",
		},
		[]string{"rule_name"},
	)

	// NodesNonCompliantTotal tracks total non-compliant nodes
	NodesNonCompliantTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeorthos_nodes_non_compliant_total",
			Help: "Total number of nodes evaluated as non-compliant against a ClusterRule.",
		},
		[]string{"rule_name"},
	)

	// NodeQuarantineStatus tracks quarantine status per node/rule
	NodeQuarantineStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kubeorthos_node_quarantine_status",
			Help: "Quarantine status of a node under a specific ClusterRule (1 for quarantined, 0 for not).",
		},
		[]string{"node", "rule_name"},
	)

	// ReclamationJobsTriggeredTotal tracks total reclamation jobs triggered
	ReclamationJobsTriggeredTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeorthos_reclamation_jobs_triggered_total",
			Help: "Total number of automated reclamation jobs triggered on a node for a specific condition.",
		},
		[]string{"node", "condition"},
	)

	// ReclamationJobFailuresTotal tracks total reclamation job failures
	ReclamationJobFailuresTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeorthos_reclamation_job_failures_total",
			Help: "Total number of automated reclamation job failures on a node.",
		},
		[]string{"node"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		NodesEvaluatedTotal,
		NodesCompliantTotal,
		NodesNonCompliantTotal,
		NodeQuarantineStatus,
		ReclamationJobsTriggeredTotal,
		ReclamationJobFailuresTotal,
	)
}

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

	originalRule := rule.DeepCopy() // Keep copy for patch updates

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
		originalNode := node.DeepCopy() // Keep original state for diff patching
		isCompliant, mismatchMsg := r.auditNodeCompliance(&rule, &node)

		// Increment evaluation and compliance metrics
		NodesEvaluatedTotal.WithLabelValues(rule.Name).Inc()
		if isCompliant {
			NodesCompliantTotal.WithLabelValues(rule.Name).Inc()
		} else {
			NodesNonCompliantTotal.WithLabelValues(rule.Name).Inc()
		}

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

		// Update Node quarantine status metric based on annotation presence after reconcileCordoning
		ruleQuarantineKey := constants.AnnotationQuarantinePrefix + rule.Name
		if node.Annotations != nil && node.Annotations[ruleQuarantineKey] == constants.ValueTrue {
			NodeQuarantineStatus.WithLabelValues(node.Name, rule.Name).Set(1)
		} else {
			NodeQuarantineStatus.WithLabelValues(node.Name, rule.Name).Set(0)
		}

		if cordonUpdated || labelUpdated {
			if err := r.Patch(ctx, &node, client.MergeFrom(originalNode)); err != nil {
				log.Error(err, "unable to patch node specs/labels/annotations", "node", node.Name)
				return ctrl.Result{}, err
			}
			log.Info("Successfully patched node state", "node", node.Name)
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
	if err := r.Status().Patch(ctx, &rule, client.MergeFrom(originalRule)); err != nil {
		log.Error(err, "unable to patch ClusterRule status")
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
	ruleQuarantineKey := constants.AnnotationQuarantinePrefix + rule.Name

	if rule.Spec.Action == auditv1alpha1.ActionEnforce {
		// Check if control plane node
		_, isCP := node.Labels[constants.LabelNodeRoleControlPlane]
		_, isMaster := node.Labels[constants.LabelNodeRoleMaster]
		isControlPlane := isCP || isMaster

		if isControlPlane {
			return false
		}

		if !isCompliant {
			if node.Annotations == nil {
				node.Annotations = make(map[string]string)
			}
			hasOurQuarantine := node.Annotations[ruleQuarantineKey] == constants.ValueTrue
			if !node.Spec.Unschedulable || !hasOurQuarantine {
				node.Spec.Unschedulable = true
				node.Annotations[ruleQuarantineKey] = constants.ValueTrue
				node.Annotations[constants.AnnotationQuarantined] = constants.ValueTrue
				nodeUpdated = true
				r.Recorder.Eventf(rule, nil, corev1.EventTypeWarning, constants.EventReasonCordonedNode, constants.EventActionEnforcement, "Successfully cordoned non-compliant worker node %s", node.Name)
			}
		} else {
			// Compliant node - check if it was quarantined by this rule
			if node.Annotations != nil {
				if _, isQuarantined := node.Annotations[ruleQuarantineKey]; isQuarantined {
					delete(node.Annotations, ruleQuarantineKey)
					nodeUpdated = true

					// Check if any other rules still quarantine this node
					hasOtherQuarantines := false
					for k, v := range node.Annotations {
						if strings.HasPrefix(k, constants.AnnotationQuarantinePrefix) && v == constants.ValueTrue {
							hasOtherQuarantines = true
							break
						}
					}

					if !hasOtherQuarantines {
						node.Spec.Unschedulable = false
						delete(node.Annotations, constants.AnnotationQuarantined)
						r.Recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonUncordonedNode, constants.EventActionEnforcement, "Successfully uncordoned compliant worker node %s", node.Name)
					} else {
						// Node remains cordoned due to other rules, but our quarantine claim is released
						r.Recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonUncordonedNode, constants.EventActionEnforcement, "Released quarantine claim on worker node %s (remains cordoned by other rules)", node.Name)
					}
				}
			}
		}
	} else {
		// Rule is in Audit mode (or not Enforce) - lift our rule-specific quarantine claim if it exists
		if node.Annotations != nil {
			if _, isQuarantined := node.Annotations[ruleQuarantineKey]; isQuarantined {
				delete(node.Annotations, ruleQuarantineKey)
				nodeUpdated = true

				// Check if any other rules still quarantine this node
				hasOtherQuarantines := false
				for k, v := range node.Annotations {
					if strings.HasPrefix(k, constants.AnnotationQuarantinePrefix) && v == constants.ValueTrue {
						hasOtherQuarantines = true
						break
					}
				}

				if !hasOtherQuarantines {
					node.Spec.Unschedulable = false
					delete(node.Annotations, constants.AnnotationQuarantined)
					r.Recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonUncordonedNode, constants.EventActionEnforcement, "Transitioned to Audit: Successfully uncordoned worker node %s", node.Name)
				} else {
					r.Recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonUncordonedNode, constants.EventActionEnforcement, "Transitioned to Audit: Released quarantine claim on worker node %s (remains cordoned by other rules)", node.Name)
				}
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

	jobNamespace := rule.Namespace
	if jobNamespace == "" {
		jobNamespace = "default"
	}

	jobList := &batchv1.JobList{}
	labelSelector := labels.SelectorFromSet(labels.Set{
		constants.LabelReclamationNode: node.Name,
		constants.LabelClusterRule:     rule.Name,
	})
	if err := r.List(ctx, jobList, client.InNamespace(jobNamespace), client.MatchingLabelsSelector{Selector: labelSelector}); err != nil {
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

		job := utils.NewReclamationJob(jobName, jobNamespace, node.Name, script, jobLabels)

		if err := r.Create(ctx, job); err != nil {
			log.Error(err, "unable to create reclamation job", "job", jobName)
			return err
		}
		ReclamationJobsTriggeredTotal.WithLabelValues(node.Name, "DiskPressure").Inc()
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
			ReclamationJobFailuresTotal.WithLabelValues(node.Name).Inc()
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
			builder.WithPredicates(nodePredicate{}),
		).
		Named("clusterrule").
		Complete(r)
}

// nodePredicate filters Node update events to ignore heartbeats and only trigger on relevant changes
type nodePredicate struct {
	predicate.Funcs
}

// Create returns true for new Node creations
func (nodePredicate) Create(e event.CreateEvent) bool {
	return true
}

// Delete returns true for Node deletions
func (nodePredicate) Delete(e event.DeleteEvent) bool {
	return true
}

// Update evaluates if the Node update is relevant to KubeOrthos auditing
func (nodePredicate) Update(e event.UpdateEvent) bool {
	oldNode, okOld := e.ObjectOld.(*corev1.Node)
	newNode, okNew := e.ObjectNew.(*corev1.Node)
	if !okOld || !okNew {
		return false
	}

	// 1. Check labels and annotations changes
	if !reflect.DeepEqual(oldNode.Labels, newNode.Labels) {
		return true
	}
	if !reflect.DeepEqual(oldNode.Annotations, newNode.Annotations) {
		return true
	}

	// 2. Check spec changes (e.g. cordoning)
	if oldNode.Spec.Unschedulable != newNode.Spec.Unschedulable {
		return true
	}

	// 3. Check system metadata changes
	if oldNode.Status.NodeInfo.KubeletVersion != newNode.Status.NodeInfo.KubeletVersion ||
		oldNode.Status.NodeInfo.ContainerRuntimeVersion != newNode.Status.NodeInfo.ContainerRuntimeVersion ||
		oldNode.Status.NodeInfo.KernelVersion != newNode.Status.NodeInfo.KernelVersion ||
		oldNode.Status.NodeInfo.OSImage != newNode.Status.NodeInfo.OSImage ||
		oldNode.Status.NodeInfo.Architecture != newNode.Status.NodeInfo.Architecture {
		return true
	}

	// 4. Check core status condition changes (ignoring timestamps)
	for _, condType := range []corev1.NodeConditionType{
		corev1.NodeReady,
		corev1.NodeMemoryPressure,
		corev1.NodeDiskPressure,
		corev1.NodePIDPressure,
		corev1.NodeNetworkUnavailable,
	} {
		var oldStatus corev1.ConditionStatus
		var newStatus corev1.ConditionStatus
		for _, c := range oldNode.Status.Conditions {
			if c.Type == condType {
				oldStatus = c.Status
				break
			}
		}
		for _, c := range newNode.Status.Conditions {
			if c.Type == condType {
				newStatus = c.Status
				break
			}
		}
		if oldStatus != newStatus {
			return true
		}
	}

	// 5. Check allocatable resources changes
	for _, resName := range []corev1.ResourceName{
		corev1.ResourceCPU,
		corev1.ResourceMemory,
		corev1.ResourceEphemeralStorage,
	} {
		oldQty := oldNode.Status.Allocatable[resName]
		newQty := newNode.Status.Allocatable[resName]
		if oldQty.Cmp(newQty) != 0 {
			return true
		}
	}

	return false
}

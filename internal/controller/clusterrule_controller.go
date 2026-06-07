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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
	"kubeorthos/internal/constants"

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
		isCompliant, mismatchMsg := auditNodeCompliance(r.Client, r.Recorder, &rule, &node)

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

		cordonUpdated := reconcileCordoning(r.Client, r.Recorder, &rule, &node, isCompliant)
		labelUpdated := reconcileLabeling(r.Client, r.Recorder, &rule, &node, isCompliant)

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
		if err := reconcileReclamation(ctx, r.Client, r.Recorder, &rule, &node); err != nil {
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

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterRuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&auditv1alpha1.ClusterRule{}).
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				node, ok := obj.(*corev1.Node)
				if !ok {
					return nil
				}
				rulesMap := make(map[string]bool)
				for k := range node.Labels {
					if after, ok0 := strings.CutPrefix(k, "compliance.kubeorthos.io/"); ok0 {
						rulesMap[after] = true
					}
				}
				for k := range node.Annotations {
					if after, ok0 := strings.CutPrefix(k, "quarantine.kubeorthos.io/"); ok0 {
						rulesMap[after] = true
					}
				}
				var requests []reconcile.Request
				for ruleName := range rulesMap {
					requests = append(requests, reconcile.Request{
						NamespacedName: client.ObjectKey{
							Name: ruleName,
						},
					})
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

// Create returns true for Node creations if they carry compliance or quarantine labels/annotations
func (nodePredicate) Create(e event.CreateEvent) bool {
	if e.Object == nil {
		return false
	}
	for k := range e.Object.GetLabels() {
		if strings.HasPrefix(k, "compliance.kubeorthos.io/") || strings.HasPrefix(k, "quarantine.kubeorthos.io/") {
			return true
		}
	}
	for k := range e.Object.GetAnnotations() {
		if strings.HasPrefix(k, "compliance.kubeorthos.io/") || strings.HasPrefix(k, "quarantine.kubeorthos.io/") {
			return true
		}
	}
	return false
}

// Delete returns true for Node deletions if they carried compliance or quarantine labels/annotations
func (nodePredicate) Delete(e event.DeleteEvent) bool {
	if e.Object == nil {
		return false
	}
	for k := range e.Object.GetLabels() {
		if strings.HasPrefix(k, "compliance.kubeorthos.io/") || strings.HasPrefix(k, "quarantine.kubeorthos.io/") {
			return true
		}
	}
	for k := range e.Object.GetAnnotations() {
		if strings.HasPrefix(k, "compliance.kubeorthos.io/") || strings.HasPrefix(k, "quarantine.kubeorthos.io/") {
			return true
		}
	}
	return false
}

// Update evaluates if the Node update contains changes to compliance or quarantine labels/annotations
func (nodePredicate) Update(e event.UpdateEvent) bool {
	oldNode, okOld := e.ObjectOld.(*corev1.Node)
	newNode, okNew := e.ObjectNew.(*corev1.Node)
	if !okOld || !okNew {
		return false
	}

	return hasSelectivePrefixChange(oldNode.Labels, newNode.Labels) ||
		hasSelectivePrefixChange(oldNode.Annotations, newNode.Annotations)
}

func hasSelectivePrefixChange(oldMap, newMap map[string]string) bool {
	for k, v := range newMap {
		if strings.HasPrefix(k, "compliance.kubeorthos.io/") || strings.HasPrefix(k, "quarantine.kubeorthos.io/") {
			if oldVal, exists := oldMap[k]; !exists || oldVal != v {
				return true
			}
		}
	}
	for k := range oldMap {
		if strings.HasPrefix(k, "compliance.kubeorthos.io/") || strings.HasPrefix(k, "quarantine.kubeorthos.io/") {
			if _, exists := newMap[k]; !exists {
				return true
			}
		}
	}
	return false
}

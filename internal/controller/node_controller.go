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
	"reflect"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
	"kubeorthos/internal/constants"
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

// NodeReconciler reconciles a Node object
type NodeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=audit.kubeorthos.io,resources=clusterrules,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the main reconciliation loop for Kubernetes Node resources
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the Node instance
	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch Node")
		return ctrl.Result{}, err
	}

	originalNode := node.DeepCopy()

	// Exclude control plane / master nodes from NodeReconciler
	_, isCP := node.Labels[constants.LabelNodeRoleControlPlane]
	_, isMaster := node.Labels[constants.LabelNodeRoleMaster]
	if isCP || isMaster {
		log.Info("Skipping control plane node in NodeReconciler", "node", node.Name)
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling Node", "name", node.Name)

	// List all ClusterRules in the cache
	var rules auditv1alpha1.ClusterRuleList
	if err := r.List(ctx, &rules); err != nil {
		log.Error(err, "unable to list ClusterRules")
		return ctrl.Result{}, err
	}

	var reclamationErr error
	activeRules := make(map[string]struct{}, len(rules.Items))

	// Evaluate the node against each rule's criteria
	for _, rule := range rules.Items {
		// Treat terminating ClusterRules as inactive and clean up their state on this node
		if !rule.DeletionTimestamp.IsZero() {
			cleanupRuleFromNode(r.Recorder, &rule, &node)
			continue
		}
		activeRules[rule.Name] = struct{}{}

		// Evaluate if the node matches the rule's selector
		matches := true
		if rule.Spec.NodeSelector != nil {
			selector, err := metav1.LabelSelectorAsSelector(rule.Spec.NodeSelector)
			if err != nil {
				log.Error(err, "unable to parse NodeSelector", "rule", rule.Name)
				continue
			}
			matches = selector.Matches(labels.Set(node.Labels))
		}

		if matches {
			isCompliant, mismatchMsg := auditNodeCompliance(&rule, &node)

			// Stamp compliance tracking label: compliance.kubeorthos.io/<rule-name>: "true"|"false"
			complianceLabelKey := constants.LabelCompliancePrefix + rule.Name
			if node.Labels == nil {
				node.Labels = make(map[string]string)
			}
			complianceVal := "false"
			if isCompliant {
				complianceVal = constants.ValueTrue
			}
			node.Labels[complianceLabelKey] = complianceVal

			// Metrics
			NodesEvaluatedTotal.WithLabelValues(rule.Name).Inc()
			if isCompliant {
				NodesCompliantTotal.WithLabelValues(rule.Name).Inc()
			} else {
				NodesNonCompliantTotal.WithLabelValues(rule.Name).Inc()
			}

			log.Info("Audited node against ClusterRule expectations in NodeReconciler",
				"node", node.Name,
				"rule", rule.Name,
				"isCompliant", isCompliant,
			)

			if !isCompliant {
				log.Info("Node is non-compliant in NodeReconciler", "node", node.Name, "rule", rule.Name, "mismatches", mismatchMsg)
				switch rule.Spec.Action {
				case auditv1alpha1.ActionAudit:
					r.Recorder.Eventf(&rule, nil, corev1.EventTypeWarning, constants.EventReasonNonCompliantNode, constants.EventActionAudit, "Node %s is non-compliant: %s", node.Name, mismatchMsg)
				case auditv1alpha1.ActionEnforce:
					r.Recorder.Eventf(&rule, nil, corev1.EventTypeWarning, constants.EventReasonNonCompliantNodeEnforced, constants.EventActionEnforce, "Node %s is non-compliant and is being enforced: %s", node.Name, mismatchMsg)
				}
			}

			// Quarantine or unquarantine
			reconcileCordoning(r.Recorder, &rule, &node, isCompliant)
			reconcileLabeling(r.Recorder, &rule, &node, isCompliant)

			// Update Node quarantine status metric
			ruleQuarantineKey := constants.AnnotationQuarantinePrefix + rule.Name
			if node.Annotations != nil && node.Annotations[ruleQuarantineKey] == constants.ValueTrue {
				NodeQuarantineStatus.WithLabelValues(node.Name, rule.Name).Set(1)
			} else {
				NodeQuarantineStatus.WithLabelValues(node.Name, rule.Name).Set(0)
			}

			// Reclamation
			if err := reconcileReclamation(ctx, r.Client, r.Recorder, &rule, &node); err != nil {
				log.Error(err, "unable to reconcile reclamation", "node", node.Name, "rule", rule.Name)
				reclamationErr = err
			}
		} else {
			// Node is not targeted by this rule. Clean up our label and quarantine annotations
			cleanupRuleFromNode(r.Recorder, &rule, &node)
		}
	}

	// Sweep any orphaned compliance/quarantine state left by rules that no longer exist
	cleanupOrphanedRuleState(&node, activeRules)

	// Strategic JSON Patching: Patch node if there were any changes
	if !reflect.DeepEqual(originalNode.Labels, node.Labels) ||
		!reflect.DeepEqual(originalNode.Annotations, node.Annotations) ||
		originalNode.Spec.Unschedulable != node.Spec.Unschedulable {

		if err := r.Patch(ctx, &node, client.MergeFrom(originalNode)); err != nil {
			log.Error(err, "unable to patch node specs/labels/annotations", "node", node.Name)
			return ctrl.Result{}, err
		}
		log.Info("Successfully patched node state in NodeReconciler", "node", node.Name)
	}

	if reclamationErr != nil {
		return ctrl.Result{}, reclamationErr
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}, builder.WithPredicates(nodeReconcilePredicate{})).
		Watches(
			&auditv1alpha1.ClusterRule{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				// Reconcile all Nodes when any ClusterRule changes
				var nodes corev1.NodeList
				if err := r.List(ctx, &nodes); err != nil {
					return nil
				}
				requests := make([]reconcile.Request, len(nodes.Items))
				for i, node := range nodes.Items {
					requests[i] = reconcile.Request{
						NamespacedName: client.ObjectKey{
							Name: node.Name,
						},
					}
				}
				return requests
			}),
		).
		Named("node").
		Complete(r)
}

// nodeReconcilePredicate filters Node events for NodeReconciler, ignoring frequent
// heartbeat timestamp updates while reacting to genuine state changes.
type nodeReconcilePredicate struct {
	predicate.Funcs
}

// Create approves all non-nil Node creations so newly joined nodes are evaluated immediately.
func (nodeReconcilePredicate) Create(e event.CreateEvent) bool {
	return e.Object != nil
}

// Delete ignores Node deletions since NodeReconciler only mutates live Nodes.
func (nodeReconcilePredicate) Delete(_ event.DeleteEvent) bool {
	return false
}

// Generic ignores generic events.
func (nodeReconcilePredicate) Generic(_ event.GenericEvent) bool {
	return false
}

// Update approves Node updates that change spec, labels, quarantine annotations,
// node system info, allocatable resources, or condition statuses (ignoring heartbeat timestamps).
func (nodeReconcilePredicate) Update(e event.UpdateEvent) bool {
	oldNode, okOld := e.ObjectOld.(*corev1.Node)
	newNode, okNew := e.ObjectNew.(*corev1.Node)
	if !okOld || !okNew {
		return false
	}

	if oldNode.Spec.Unschedulable != newNode.Spec.Unschedulable {
		return true
	}
	if !reflect.DeepEqual(oldNode.Labels, newNode.Labels) {
		return true
	}
	if hasQuarantineAnnotationChange(oldNode.Annotations, newNode.Annotations) {
		return true
	}
	if oldNode.Status.NodeInfo != newNode.Status.NodeInfo {
		return true
	}
	if !reflect.DeepEqual(oldNode.Status.Allocatable, newNode.Status.Allocatable) {
		return true
	}
	return nodeConditionStatusChanged(oldNode.Status.Conditions, newNode.Status.Conditions)
}

func isQuarantineAnnotationKey(k string) bool {
	return k == constants.AnnotationQuarantined || strings.HasPrefix(k, constants.AnnotationQuarantinePrefix)
}

func hasQuarantineAnnotationChange(oldMap, newMap map[string]string) bool {
	for k, v := range newMap {
		if isQuarantineAnnotationKey(k) {
			if oldVal, exists := oldMap[k]; !exists || oldVal != v {
				return true
			}
		}
	}
	for k := range oldMap {
		if isQuarantineAnnotationKey(k) {
			if _, exists := newMap[k]; !exists {
				return true
			}
		}
	}
	return false
}

func nodeConditionStatusChanged(oldConds, newConds []corev1.NodeCondition) bool {
	if len(oldConds) != len(newConds) {
		return true
	}
	oldMap := make(map[corev1.NodeConditionType]corev1.ConditionStatus, len(oldConds))
	for _, c := range oldConds {
		oldMap[c.Type] = c.Status
	}
	for _, c := range newConds {
		if oldStatus, exists := oldMap[c.Type]; !exists || oldStatus != c.Status {
			return true
		}
	}
	return false
}

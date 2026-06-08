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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
	"kubeorthos/internal/constants"
)

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

	// Evaluate the node against each rule's criteria
	for _, rule := range rules.Items {
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
			}
		} else {
			// Node is not targeted by this rule. Clean up our label and quarantine annotations
			complianceLabelKey := constants.LabelCompliancePrefix + rule.Name
			if node.Labels != nil {
				delete(node.Labels, complianceLabelKey)
			}

			// Clean up compliance/custom labels by calling reconcileLabeling with isCompliant = false
			reconcileLabeling(r.Recorder, &rule, &node, false)

			// Release quarantine claim
			ruleCopy := rule.DeepCopy()
			ruleCopy.Spec.Action = auditv1alpha1.ActionAudit
			reconcileCordoning(r.Recorder, ruleCopy, &node, true)

			// Update Node quarantine status metric
			NodeQuarantineStatus.WithLabelValues(node.Name, rule.Name).Set(0)
		}
	}

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

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}, builder.WithPredicates(nodePredicate{})).
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

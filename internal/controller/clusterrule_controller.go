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
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

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
		isCompliant := true
		var mismatchMsg string

		// Check KubeletVersion
		if rule.Spec.ExpectedNodeConfig.KubeletVersion != "" && node.Status.NodeInfo.KubeletVersion != rule.Spec.ExpectedNodeConfig.KubeletVersion {
			isCompliant = false
			mismatchMsg += fmt.Sprintf("KubeletVersion mismatch: expected %s, got %s. ", rule.Spec.ExpectedNodeConfig.KubeletVersion, node.Status.NodeInfo.KubeletVersion)
		}

		// Check ContainerRuntime
		if rule.Spec.ExpectedNodeConfig.ContainerRuntime != "" && node.Status.NodeInfo.ContainerRuntimeVersion != rule.Spec.ExpectedNodeConfig.ContainerRuntime {
			isCompliant = false
			mismatchMsg += fmt.Sprintf("ContainerRuntime mismatch: expected %s, got %s. ", rule.Spec.ExpectedNodeConfig.ContainerRuntime, node.Status.NodeInfo.ContainerRuntimeVersion)
		}

		// Check KernelVersion
		if rule.Spec.ExpectedNodeConfig.KernelVersion != "" && node.Status.NodeInfo.KernelVersion != rule.Spec.ExpectedNodeConfig.KernelVersion {
			isCompliant = false
			mismatchMsg += fmt.Sprintf("KernelVersion mismatch: expected %s, got %s. ", rule.Spec.ExpectedNodeConfig.KernelVersion, node.Status.NodeInfo.KernelVersion)
		}

		// Check OSImage
		if rule.Spec.ExpectedNodeConfig.OSImage != "" && node.Status.NodeInfo.OSImage != rule.Spec.ExpectedNodeConfig.OSImage {
			isCompliant = false
			mismatchMsg += fmt.Sprintf("OSImage mismatch: expected %s, got %s. ", rule.Spec.ExpectedNodeConfig.OSImage, node.Status.NodeInfo.OSImage)
		}

		// Check Architecture
		if rule.Spec.ExpectedNodeConfig.Architecture != "" && node.Status.NodeInfo.Architecture != rule.Spec.ExpectedNodeConfig.Architecture {
			isCompliant = false
			mismatchMsg += fmt.Sprintf("Architecture mismatch: expected %s, got %s. ", rule.Spec.ExpectedNodeConfig.Architecture, node.Status.NodeInfo.Architecture)
		}

		// Check ExpectedConditions
		for _, reqCond := range rule.Spec.ExpectedConditions {
			var found bool
			for _, nodeCond := range node.Status.Conditions {
				if nodeCond.Type == reqCond.Type {
					found = true
					if nodeCond.Status != reqCond.Status {
						isCompliant = false
						mismatchMsg += fmt.Sprintf("Node Condition %s is %s, expected %s. ", reqCond.Type, nodeCond.Status, reqCond.Status)
					}
					break
				}
			}
			if !found {
				isCompliant = false
				mismatchMsg += fmt.Sprintf("Node Condition %s is missing, expected %s. ", reqCond.Type, reqCond.Status)
			}
		}

		// Check MinimumResources
		if rule.Spec.MinimumResources != nil {
			if rule.Spec.MinimumResources.CPU != nil {
				actualCPU := node.Status.Allocatable[corev1.ResourceCPU]
				if actualCPU.Cmp(*rule.Spec.MinimumResources.CPU) < 0 {
					isCompliant = false
					mismatchMsg += fmt.Sprintf("Allocatable CPU is insufficient: expected %s, got %s. ", rule.Spec.MinimumResources.CPU.String(), actualCPU.String())
				}
			}
			if rule.Spec.MinimumResources.Memory != nil {
				actualMemory := node.Status.Allocatable[corev1.ResourceMemory]
				if actualMemory.Cmp(*rule.Spec.MinimumResources.Memory) < 0 {
					isCompliant = false
					mismatchMsg += fmt.Sprintf("Allocatable Memory is insufficient: expected %s, got %s. ", rule.Spec.MinimumResources.Memory.String(), actualMemory.String())
				}
			}
			if rule.Spec.MinimumResources.Storage != nil {
				actualStorage := node.Status.Allocatable[corev1.ResourceEphemeralStorage]
				if actualStorage.Cmp(*rule.Spec.MinimumResources.Storage) < 0 {
					isCompliant = false
					mismatchMsg += fmt.Sprintf("Allocatable Ephemeral Storage is insufficient: expected %s, got %s. ", rule.Spec.MinimumResources.Storage.String(), actualStorage.String())
				}
			}
		}

		if !isCompliant {
			nonCompliantNodes = append(nonCompliantNodes, node.Name)
			log.Info("Node is non-compliant", "node", node.Name, "mismatches", mismatchMsg)
			if rule.Spec.Action == auditv1alpha1.ActionAudit {
				r.Recorder.Eventf(&rule, nil, corev1.EventTypeWarning, "NonCompliantNode", "ActionAudit", "Node %s is non-compliant: %s", node.Name, mismatchMsg)
			}
		}
	}

	// Update Status
	condition := metav1.Condition{
		Type:               "Compliant",
		Status:             metav1.ConditionTrue,
		Reason:             "AllNodesCompliant",
		Message:            "All nodes match the expected configuration",
		ObservedGeneration: rule.Generation,
	}

	if rule.Spec.NodeSelector != nil && len(nodeList.Items) == 0 {
		condition.Reason = "NoMatchingNodes"
		condition.Message = "No nodes matched the specified node selector"
	} else if len(nonCompliantNodes) > 0 {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "NodesNonCompliant"
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

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

package v1alpha1

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var clusterrulelog = logf.Log.WithName("clusterrule-resource")

// SetupClusterRuleWebhookWithManager registers the webhook for ClusterRule in the manager.
func SetupClusterRuleWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &auditv1alpha1.ClusterRule{}).
		WithValidator(&ClusterRuleCustomValidator{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-audit-kubeorthos-io-v1alpha1-clusterrule,mutating=false,failurePolicy=fail,sideEffects=None,groups=audit.kubeorthos.io,resources=clusterrules,verbs=create;update,versions=v1alpha1,name=vclusterrule-v1alpha1.kb.io,admissionReviewVersions=v1

// ClusterRuleCustomValidator struct is responsible for validating the ClusterRule resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type ClusterRuleCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

// validateClusterRule performs all spec validation for ClusterRule
func validateClusterRule(rule *auditv1alpha1.ClusterRule) error {
	var allErrs field.ErrorList

	// 1. Validate NodeSelector using metav1.LabelSelectorAsSelector
	if rule.Spec.NodeSelector != nil {
		_, err := metav1.LabelSelectorAsSelector(rule.Spec.NodeSelector)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec").Child("nodeSelector"),
				rule.Spec.NodeSelector,
				err.Error(),
			))
		}
	}

	// 2. Validate ExpectedNodeConfig is not empty
	config := rule.Spec.ExpectedNodeConfig
	if config.KubeletVersion == "" &&
		config.ContainerRuntime == "" &&
		config.KernelVersion == "" &&
		config.OSImage == "" &&
		config.Architecture == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec").Child("expectedNodeConfig"),
			"expectedNodeConfig cannot be empty; at least one expected configuration field must be specified",
		))
	}

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type ClusterRule.
func (v *ClusterRuleCustomValidator) ValidateCreate(_ context.Context, obj *auditv1alpha1.ClusterRule) (admission.Warnings, error) {
	clusterrulelog.Info("Validation for ClusterRule upon creation", "name", obj.GetName())
	err := validateClusterRule(obj)
	return nil, err
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type ClusterRule.
func (v *ClusterRuleCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *auditv1alpha1.ClusterRule) (admission.Warnings, error) {
	clusterrulelog.Info("Validation for ClusterRule upon update", "name", newObj.GetName())
	err := validateClusterRule(newObj)
	return nil, err
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type ClusterRule.
func (v *ClusterRuleCustomValidator) ValidateDelete(_ context.Context, obj *auditv1alpha1.ClusterRule) (admission.Warnings, error) {
	clusterrulelog.Info("Validation for ClusterRule upon deletion", "name", obj.GetName())
	return nil, nil
}

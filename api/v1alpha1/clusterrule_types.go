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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ExpectedNodeConfig defines the node configurations we expect to find
type ExpectedNodeConfig struct {
	// KubeletVersion specifies the expected version of the kubelet
	// +optional
	KubeletVersion string `json:"kubeletVersion,omitempty"`

	// ContainerRuntime specifies the expected container runtime string
	// +optional
	ContainerRuntime string `json:"containerRuntime,omitempty"`

	// KernelVersion specifies the expected kernel version of the node
	// +optional
	KernelVersion string `json:"kernelVersion,omitempty"`

	// OSImage specifies the expected OS image of the node
	// +optional
	OSImage string `json:"osImage,omitempty"`

	// Architecture specifies the expected CPU architecture of the node
	// +optional
	Architecture string `json:"architecture,omitempty"`
}

// ActionType defines the type of action to take when a rule is evaluated
// +kubebuilder:validation:Enum=Audit;Enforce
type ActionType string

const (
	ActionAudit   ActionType = "Audit"
	ActionEnforce ActionType = "Enforce"
)

// ClusterRuleSpec defines the desired state of ClusterRule
type ClusterRuleSpec struct {
	// Action specifies what to do when a node does not match the expected configuration
	// +kubebuilder:validation:Required
	Action ActionType `json:"action"`

	// ExpectedNodeConfig specifies the expected configuration for the nodes
	// +kubebuilder:validation:Required
	ExpectedNodeConfig ExpectedNodeConfig `json:"expectedNodeConfig"`

	// NodeSelector specifies a label selector to target specific nodes.
	// If empty, the rule applies to all nodes.
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
}

// ClusterRuleStatus defines the observed state of ClusterRule.
type ClusterRuleStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the ClusterRule resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ClusterRule is the Schema for the clusterrules API
type ClusterRule struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ClusterRule
	// +required
	Spec ClusterRuleSpec `json:"spec"`

	// status defines the observed state of ClusterRule
	// +optional
	Status ClusterRuleStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ClusterRuleList contains a list of ClusterRule
type ClusterRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ClusterRule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterRule{}, &ClusterRuleList{})
}

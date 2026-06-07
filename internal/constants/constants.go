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

package constants

// Condition related constants used in the ClusterRule controller
const (
	// ConditionTypeCompliant is the Type field for the ClusterRule compliance status condition
	ConditionTypeCompliant = "Compliant"

	// ConditionReasonAllNodesCompliant indicates that all evaluated nodes match the expected configuration
	ConditionReasonAllNodesCompliant = "AllNodesCompliant"

	// ConditionReasonNoMatchingNodes indicates that no nodes in the cluster matched the target selector
	ConditionReasonNoMatchingNodes = "NoMatchingNodes"

	// ConditionReasonNodesNonCompliant indicates that one or more evaluated nodes failed compliance checks
	ConditionReasonNodesNonCompliant = "NodesNonCompliant"
)

// Annotation and Label keys used in node management and tracking
const (
	// AnnotationQuarantined is applied to worker nodes cordoned by KubeOrthos to track operator ownership
	AnnotationQuarantined = "policy.kubeorthos.io/quarantined"

	// AnnotationQuarantinePrefix is the prefix used for scoping quarantine annotations to specific rules (e.g. quarantine.kubeorthos.io/<rule>)
	AnnotationQuarantinePrefix = "quarantine.kubeorthos.io/"

	// ValueTrue is the standard string value for "true" used in labels and annotations
	ValueTrue = "true"

	// LabelReclamationNode matches the target node name for automated resource reclamation Jobs
	LabelReclamationNode = "policy.kubeorthos.io/reclamation-node"

	// LabelClusterRule matches the parent ClusterRule resource for automated resource reclamation Jobs
	LabelClusterRule = "policy.kubeorthos.io/clusterrule"

	// LabelNodeRoleControlPlane is the standard label indicating a control-plane node
	LabelNodeRoleControlPlane = "node-role.kubernetes.io/control-plane"

	// LabelNodeRoleMaster is the legacy label indicating a control-plane/master node
	LabelNodeRoleMaster = "node-role.kubernetes.io/master"

	// LabelCompliancePrefix is the prefix used for tracking compliance labels per rule (e.g. compliance.kubeorthos.io/<rule>)
	LabelCompliancePrefix = "compliance.kubeorthos.io/"
)

// Event Reasons used when emitting Kubernetes Events
const (
	// EventReasonNonCompliantNode is emitted when a node is audited and found non-compliant
	EventReasonNonCompliantNode = "NonCompliantNode"

	// EventReasonNonCompliantNodeEnforced is emitted when a non-compliant node triggers active enforcement
	EventReasonNonCompliantNodeEnforced = "NonCompliantNodeEnforced"

	// EventReasonComplianceLabelApplied is emitted when the standard compliance label is added to a compliant node
	EventReasonComplianceLabelApplied = "ComplianceLabelApplied"

	// EventReasonCustomLabelApplied is emitted when a custom label is added to a compliant node
	EventReasonCustomLabelApplied = "CustomLabelApplied"

	// EventReasonComplianceLabelRemoved is emitted when the compliance label is stripped from a non-compliant node
	EventReasonComplianceLabelRemoved = "ComplianceLabelRemoved"

	// EventReasonCustomLabelRemoved is emitted when a custom label is stripped from a non-compliant node
	EventReasonCustomLabelRemoved = "CustomLabelRemoved"

	// EventReasonCordonedNode is emitted when a worker node is successfully cordoned under Enforce mode
	EventReasonCordonedNode = "CordonedNode"

	// EventReasonUncordonedNode is emitted when a worker node is uncordoned (compliant again or rule transitioned)
	EventReasonUncordonedNode = "UncordonedNode"

	// EventReasonReclamationTriggered is emitted when an automated resource reclamation Job is spawned
	EventReasonReclamationTriggered = "ReclamationTriggered"

	// EventReasonReclamationCompleted is emitted when an automated resource reclamation Job completes successfully
	EventReasonReclamationCompleted = "ReclamationCompleted"

	// EventReasonReclamationFailed is emitted when an automated resource reclamation Job fails execution
	EventReasonReclamationFailed = "ReclamationFailed"
)

// Event Action/Component categories
const (
	// EventActionAudit represents auditing events
	EventActionAudit = "ActionAudit"

	// EventActionEnforce represents active enforcement events
	EventActionEnforce = "ActionEnforce"

	// EventActionComplianceLabel represents compliance labeling events
	EventActionComplianceLabel = "ComplianceLabel"

	// EventActionEnforcement represents cordoning enforcement events
	EventActionEnforcement = "Enforcement"

	// EventActionReclamation represents resource reclamation events
	EventActionReclamation = "Reclamation"
)

// Defaults and system thresholds
const (
	// DefaultLogSizeLimit is the fallback size threshold for log truncation (e.g. "100Mi")
	DefaultLogSizeLimit = "100Mi"

	// DefaultReclamationImage is the base image used for node reclamation Jobs
	DefaultReclamationImage = "alpine:3.18"
)

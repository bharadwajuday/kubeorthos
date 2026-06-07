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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
	"kubeorthos/internal/constants"
	"kubeorthos/internal/utils"
)

// auditNodeCompliance checks if a node complies with the expectations of the ClusterRule
func auditNodeCompliance(rule *auditv1alpha1.ClusterRule, node *corev1.Node) (bool, string) {
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
func reconcileCordoning(recorder events.EventRecorder, rule *auditv1alpha1.ClusterRule, node *corev1.Node, isCompliant bool) bool {
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
				recorder.Eventf(rule, nil, corev1.EventTypeWarning, constants.EventReasonCordonedNode, constants.EventActionEnforcement, "Successfully cordoned non-compliant worker node %s", node.Name)
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
						recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonUncordonedNode, constants.EventActionEnforcement, "Successfully uncordoned compliant worker node %s", node.Name)
					} else {
						// Node remains cordoned due to other rules, but our quarantine claim is released
						recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonUncordonedNode, constants.EventActionEnforcement, "Released quarantine claim on worker node %s (remains cordoned by other rules)", node.Name)
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
					recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonUncordonedNode, constants.EventActionEnforcement, "Transitioned to Audit: Successfully uncordoned worker node %s", node.Name)
				} else {
					recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonUncordonedNode, constants.EventActionEnforcement, "Transitioned to Audit: Released quarantine claim on worker node %s (remains cordoned by other rules)", node.Name)
				}
			}
		}
	}

	return nodeUpdated
}

// reconcileLabeling applies or removes compliance/custom labels on the node
func reconcileLabeling(recorder events.EventRecorder, rule *auditv1alpha1.ClusterRule, node *corev1.Node, isCompliant bool) bool {
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
				recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonComplianceLabelApplied, constants.EventActionComplianceLabel, "Successfully applied label %s=%s to node %s", labelKey, labelValue, node.Name)
			}
		}
		// Node is compliant, ensure custom labels are applied
		for k, v := range rule.Spec.CustomLabels {
			if val, exists := nodeLabels[k]; !exists || val != v {
				nodeLabels[k] = v
				nodeUpdated = true
				recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonCustomLabelApplied, constants.EventActionComplianceLabel, "Successfully applied custom label %s=%s to node %s", k, v, node.Name)
			}
		}
	} else {
		// Node is non-compliant, ensure compliance label is removed
		if rule.Spec.ComplianceLabel != nil {
			labelKey := rule.Spec.ComplianceLabel.Key
			if _, exists := nodeLabels[labelKey]; exists {
				delete(nodeLabels, labelKey)
				nodeUpdated = true
				recorder.Eventf(rule, nil, corev1.EventTypeWarning, constants.EventReasonComplianceLabelRemoved, constants.EventActionComplianceLabel, "Successfully removed label %s from non-compliant node %s", labelKey, node.Name)
			}
		}
		// Node is non-compliant, ensure custom labels are removed
		for k := range rule.Spec.CustomLabels {
			if _, exists := nodeLabels[k]; exists {
				delete(nodeLabels, k)
				nodeUpdated = true
				recorder.Eventf(rule, nil, corev1.EventTypeWarning, constants.EventReasonCustomLabelRemoved, constants.EventActionComplianceLabel, "Successfully removed custom label %s from non-compliant node %s", k, node.Name)
			}
		}
	}

	if nodeUpdated {
		node.SetLabels(nodeLabels)
	}

	return nodeUpdated
}

// reconcileReclamation checks for Node disk pressure and spawns/manages resource reclamation Jobs
func reconcileReclamation(ctx context.Context, c client.Client, recorder events.EventRecorder, rule *auditv1alpha1.ClusterRule, node *corev1.Node) error {
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
	if err := c.List(ctx, jobList, client.InNamespace(jobNamespace), client.MatchingLabelsSelector{Selector: labelSelector}); err != nil {
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

		if err := c.Create(ctx, job); err != nil {
			log.Error(err, "unable to create reclamation job", "job", jobName)
			return err
		}
		ReclamationJobsTriggeredTotal.WithLabelValues(node.Name, "DiskPressure").Inc()
		log.Info("Triggered automated resource reclamation Job", "node", node.Name, "job", jobName)
		recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonReclamationTriggered, constants.EventActionReclamation, "Triggered automated reclamation Job %s on node %s", jobName, node.Name)
	} else {
		// Check active Job status
		existingJob := &jobList.Items[0]
		if existingJob.Status.Succeeded > 0 {
			if err := c.Delete(ctx, existingJob); err != nil {
				log.Error(err, "unable to delete succeeded reclamation job", "job", existingJob.Name)
				return err
			}
			log.Info("Automated resource reclamation completed successfully", "node", node.Name)
			recorder.Eventf(rule, nil, corev1.EventTypeNormal, constants.EventReasonReclamationCompleted, constants.EventActionReclamation, "Automated resource reclamation completed successfully on node %s", node.Name)
		} else if existingJob.Status.Failed > 0 {
			if err := c.Delete(ctx, existingJob); err != nil {
				log.Error(err, "unable to delete failed reclamation job", "job", existingJob.Name)
				return err
			}
			ReclamationJobFailuresTotal.WithLabelValues(node.Name).Inc()
			log.Info("Automated resource reclamation failed", "node", node.Name)
			recorder.Eventf(rule, nil, corev1.EventTypeWarning, constants.EventReasonReclamationFailed, constants.EventActionReclamation, "Automated resource reclamation failed on node %s", node.Name)
		}
	}

	return nil
}

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

package utils

import (
	"fmt"
	"regexp"
	"strings"

	"kubeorthos/internal/constants"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var logSizeLimitPattern = regexp.MustCompile(`^[0-9]+[kKmMgGtTpP][iI]?$`)

// ParseLogSizeLimitBytes parses a Kubernetes-style log size threshold (e.g. "100Mi", "100M", "100k")
// into positive bytes. It normalizes unit casing so decimal suffixes like "100m" are treated as
// megabytes rather than milli-units.
func ParseLogSizeLimitBytes(limit string) (int64, error) {
	trimmed := strings.TrimSpace(limit)
	if trimmed == "" {
		return 0, fmt.Errorf("logSizeLimit cannot be empty")
	}
	if !logSizeLimitPattern.MatchString(trimmed) {
		return 0, fmt.Errorf("invalid logSizeLimit format %q: must match ^[0-9]+[kKmMgGtTpP][iI]?$", limit)
	}

	var normalized string
	if strings.HasSuffix(trimmed, "i") || strings.HasSuffix(trimmed, "I") {
		unitIdx := len(trimmed) - 2
		normalized = trimmed[:unitIdx] + strings.ToUpper(string(trimmed[unitIdx])) + "i"
	} else {
		unitIdx := len(trimmed) - 1
		unit := string(trimmed[unitIdx])
		if strings.EqualFold(unit, "k") {
			normalized = trimmed[:unitIdx] + "k"
		} else {
			normalized = trimmed[:unitIdx] + strings.ToUpper(unit)
		}
	}

	qty, err := resource.ParseQuantity(normalized)
	if err != nil {
		return 0, fmt.Errorf("failed to parse logSizeLimit %q: %w", limit, err)
	}

	bytes := qty.Value()
	if bytes <= 0 {
		return 0, fmt.Errorf("logSizeLimit %q must be greater than zero bytes", limit)
	}
	return bytes, nil
}

// BuildReclamationScript constructs a POSIX shell reclamation script for DiskPressure remediation.
func BuildReclamationScript(cleanImages, cleanContainers, cleanLogs bool, logSizeLimit string) (string, error) {
	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -ex\n")

	if cleanImages || cleanContainers {
		b.WriteString("if ! command -v crictl >/dev/null 2>&1; then\n")
		b.WriteString("  echo 'ERROR: crictl not found in reclamation image' >&2\n")
		b.WriteString("  exit 1\n")
		b.WriteString("fi\n")
	}

	if cleanImages {
		b.WriteString("echo 'Pruning containerd images...'\n")
		b.WriteString("crictl rmi --prune\n")
	}

	if cleanContainers {
		b.WriteString("echo 'Pruning stopped containers...'\n")
		b.WriteString("EXITED=$(crictl ps -a -q --state=exited)\n")
		b.WriteString("if [ -n \"$EXITED\" ]; then crictl rm $EXITED; fi\n")
	}

	if cleanLogs {
		limit := logSizeLimit
		if strings.TrimSpace(limit) == "" {
			limit = constants.DefaultLogSizeLimit
		}
		bytes, err := ParseLogSizeLimitBytes(limit)
		if err != nil {
			return "", err
		}
		b.WriteString("echo 'Truncating large logs...'\n")
		fmt.Fprintf(&b, "find /host/var/log/pods -type f -name '*.log' -size +%dc -exec truncate -s 0 {} \\;\n", bytes)
	}

	return b.String(), nil
}

// NewReclamationJob creates a Kubernetes Job designed to run a reclamation script on a specific node
func NewReclamationJob(name, namespace, nodeName, image, script string, labels map[string]string) *batchv1.Job {
	jobImage := image
	if strings.TrimSpace(jobImage) == "" {
		jobImage = constants.DefaultReclamationImage
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            Int32Ptr(1),
			TTLSecondsAfterFinished: Int32Ptr(constants.DefaultReclamationJobTTLSeconds),
			ActiveDeadlineSeconds:   Int64Ptr(constants.DefaultReclamationJobTimeoutSeconds),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					NodeName:      nodeName,
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "reclaim-agent",
							Image:   jobImage,
							Command: []string{"sh", "-c", script},
							SecurityContext: &corev1.SecurityContext{
								Privileged:               BoolPtr(false),
								AllowPrivilegeEscalation: BoolPtr(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
									Add:  []corev1.Capability{"DAC_OVERRIDE"},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "containerd-socket",
									MountPath: "/run/containerd/containerd.sock",
								},
								{
									Name:      "host-logs",
									MountPath: "/host/var/log",
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CONTAINER_RUNTIME_ENDPOINT",
									Value: "unix:///run/containerd/containerd.sock",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "containerd-socket",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/run/containerd/containerd.sock",
								},
							},
						},
						{
							Name: "host-logs",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/log",
								},
							},
						},
					},
				},
			},
		},
	}
}

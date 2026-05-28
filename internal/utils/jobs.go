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
	"kubeorthos/internal/constants"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NewReclamationJob creates a Kubernetes Job designed to run a reclamation script on a specific node
func NewReclamationJob(name, namespace, nodeName, script string, labels map[string]string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: Int32Ptr(1),
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
							Image:   constants.DefaultReclamationImage,
							Command: []string{"sh", "-c", script},
							SecurityContext: &corev1.SecurityContext{
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

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
	"strings"
	"testing"
)

func TestBoolPtr(t *testing.T) {
	val := true
	ptr := BoolPtr(val)
	if ptr == nil {
		t.Fatal("expected non-nil pointer")
	}
	if *ptr != val {
		t.Errorf("expected %t, got %t", val, *ptr)
	}
}

func TestInt32Ptr(t *testing.T) {
	var val int32 = 42
	ptr := Int32Ptr(val)
	if ptr == nil {
		t.Fatal("expected non-nil pointer")
	}
	if *ptr != val {
		t.Errorf("expected %d, got %d", val, *ptr)
	}
}

func TestInt64Ptr(t *testing.T) {
	var val int64 = 600
	ptr := Int64Ptr(val)
	if ptr == nil {
		t.Fatal("expected non-nil pointer")
	}
	if *ptr != val {
		t.Errorf("expected %d, got %d", val, *ptr)
	}
}

func TestParseLogSizeLimitBytes(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      int64
		wantError bool
	}{
		{name: "100Mi", input: "100Mi", want: 100 * 1024 * 1024},
		{name: "1Gi", input: "1Gi", want: 1024 * 1024 * 1024},
		{name: "500Ki", input: "500Ki", want: 500 * 1024},
		{name: "100M", input: "100M", want: 100 * 1000 * 1000},
		{name: "lowercase m treated as megabytes", input: "100m", want: 100 * 1000 * 1000},
		{name: "lowercase k", input: "100k", want: 100 * 1000},
		{name: "uppercase K", input: "100K", want: 100 * 1000},
		{name: "lowercase mi", input: "100mi", want: 100 * 1024 * 1024},
		{name: "zero Mi rejected", input: "0Mi", wantError: true},
		{name: "empty rejected", input: "", wantError: true},
		{name: "shell injection rejected", input: "100Mi; rm -rf /", wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLogSizeLimitBytes(tc.input)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected error for %q, got %d", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseLogSizeLimitBytes(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestBuildReclamationScript(t *testing.T) {
	t.Run("all flags enabled with 100Mi", func(t *testing.T) {
		script, err := BuildReclamationScript(true, true, true, "100Mi")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(script, "command -v crictl") {
			t.Errorf("expected crictl preflight check in script:\n%s", script)
		}
		if !strings.Contains(script, "crictl rmi --prune") {
			t.Errorf("expected crictl rmi --prune in script:\n%s", script)
		}
		if !strings.Contains(script, "--state=exited") {
			t.Errorf("expected --state=exited in script:\n%s", script)
		}
		if !strings.Contains(script, "-size +104857600c") {
			t.Errorf("expected portable byte size -size +104857600c in script:\n%s", script)
		}
	})

	t.Run("logs only skips crictl check", func(t *testing.T) {
		script, err := BuildReclamationScript(false, false, true, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(script, "crictl") {
			t.Errorf("expected no crictl references in log-only mode:\n%s", script)
		}
		if !strings.Contains(script, "-size +104857600c") {
			t.Errorf("expected default 100Mi (104857600c) in script:\n%s", script)
		}
	})

	t.Run("invalid logSizeLimit returns error", func(t *testing.T) {
		if _, err := BuildReclamationScript(false, false, true, "100Mi; rm -rf /"); err == nil {
			t.Fatal("expected error for invalid logSizeLimit")
		}
	})
}

func TestNewReclamationJob(t *testing.T) {
	name := "test-job"
	namespace := "test-ns"
	nodeName := "test-node"
	script := "echo 'hello'"
	labels := map[string]string{
		"app": "kubeorthos",
	}

	// Default image when empty
	job := NewReclamationJob(name, namespace, nodeName, "", script, labels)

	if job.Name != name {
		t.Errorf("expected job name %s, got %s", name, job.Name)
	}
	if job.Namespace != namespace {
		t.Errorf("expected job namespace %s, got %s", namespace, job.Namespace)
	}
	if job.Spec.Template.Spec.NodeName != nodeName {
		t.Errorf("expected node name %s, got %s", nodeName, job.Spec.Template.Spec.NodeName)
	}
	if job.Labels["app"] != "kubeorthos" {
		t.Errorf("expected label app=kubeorthos, got %v", job.Labels)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != constants.DefaultReclamationJobTTLSeconds {
		t.Errorf("expected TTLSecondsAfterFinished=%d, got %v", constants.DefaultReclamationJobTTLSeconds, job.Spec.TTLSecondsAfterFinished)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != constants.DefaultReclamationJobTimeoutSeconds {
		t.Errorf("expected ActiveDeadlineSeconds=%d, got %v", constants.DefaultReclamationJobTimeoutSeconds, job.Spec.ActiveDeadlineSeconds)
	}
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatal("expected exactly 1 container")
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != constants.DefaultReclamationImage {
		t.Errorf("expected default image %s, got %s", constants.DefaultReclamationImage, container.Image)
	}
	if container.SecurityContext == nil || container.SecurityContext.Privileged == nil || *container.SecurityContext.Privileged {
		t.Errorf("expected explicit Privileged=false, got %v", container.SecurityContext)
	}
	if len(container.Command) != 3 || container.Command[2] != script {
		t.Errorf("expected command script %s, got %v", script, container.Command)
	}

	// Custom image override
	customImg := "ghcr.io/bharadwajuday/kubeorthos-reclaim:v1"
	customJob := NewReclamationJob(name, namespace, nodeName, customImg, script, labels)
	if customJob.Spec.Template.Spec.Containers[0].Image != customImg {
		t.Errorf("expected custom image %s, got %s", customImg, customJob.Spec.Template.Spec.Containers[0].Image)
	}
}

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

func TestNewReclamationJob(t *testing.T) {
	name := "test-job"
	namespace := "test-ns"
	nodeName := "test-node"
	script := "echo 'hello'"
	labels := map[string]string{
		"app": "kubeorthos",
	}

	job := NewReclamationJob(name, namespace, nodeName, script, labels)

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
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatal("expected exactly 1 container")
	}
	container := job.Spec.Template.Spec.Containers[0]
	if len(container.Command) != 3 || container.Command[2] != script {
		t.Errorf("expected command script %s, got %v", script, container.Command)
	}
}

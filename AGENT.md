# KubeOrthos Agent Architecture & Developer Guide

This document (`AGENT.md`) serves as the primary architectural overview and guide for AI agents and developers working on the `KubeOrthos` project.

## Project Overview

`KubeOrthos` is a Kubernetes Operator designed to validate and ensure the configuration correctness of Kubernetes clusters against organizational and community best practices through Custom Resource Definitions (CRDs).

## Architectural Components

The application is structured following standard Go Kubernetes Operator patterns (compatible with `controller-runtime`).

### 1. `cmd/main.go`
- **Responsibility**: The entry point for the Operator manager.
- **Key Concepts**: It initializes the `controller-runtime` Manager, registers the scheme, sets up the Reconcilers, and starts the manager.

### 2. `api/` (CRDs)
- **Responsibility**: Defines the Custom Resources.
- **Key Concepts**: Contains the Go struct definitions for our CRDs (e.g., `ClusterPolicy`, `ConfigurationCheck`). These are used to generate the Kubernetes YAML manifests and DeepCopy methods.

### 3. `internal/controller`
- **Responsibility**: The Reconcile loops.
- **Key Concepts**: Watches the CRDs and standard Kubernetes resources (e.g., `Node` objects). The `ClusterRuleReconciler` specifically watches `ClusterRule` objects and maps events from `Node` changes back to the `ClusterRule`s. It evaluates the nodes' Kubelet and Container Runtime versions against the configurations specified in the CRD and updates the CRD's status with compliance conditions.

### 4. `internal/validator`
- **Responsibility**: The core rule engine.
- **Key Concepts**: Evaluates the cluster state against the rules defined in the CRDs. It identifies deviations, misconfigurations, and non-compliant resources safely.

### 5. `internal/webhook` (Future Scope)
- **Responsibility**: Admission controllers.
- **Key Concepts**: Validating or Mutating webhooks to reject non-compliant resources before they are admitted into the cluster.

## Flow of Execution

1. **Initialization**: The Operator pod starts, and the Manager begins watching registered resources. The `ClusterRuleReconciler` is registered to watch `ClusterRule` and `Node` kinds.
2. **Reconciliation Trigger**: When a `ClusterRule` is created/updated, or any watched `Node` changes, the Reconciler is triggered (via a Map function mapping Node events to all ClusterRules).
3. **Evaluation**: The Reconciler fetches all cluster `Node`s and evaluates their specifications (e.g., Kubelet version, Container Runtime) against the `ExpectedNodeConfig` specified in the `ClusterRule`.
4. **Status & Event Update**: 
   - If deviations are found, the Reconciler logs the non-compliance and emits a `Warning` Kubernetes Event on the `ClusterRule` object.
   - The Operator updates the `Status` subresource (specifically the `Conditions` array) of the `ClusterRule` to report the overall compliance state (True if compliant, False if deviations exist).

## Development Principles

- **Testability**: Every component must be unit-testable. Use `envtest` for testing controller reconciliation logic.
- **Idempotency**: The Reconcile loop must be idempotent, safely re-running without unintended side effects.
- **Event-Driven**: Rely on Kubernetes watches and informers; avoid polling where possible.

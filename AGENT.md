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
- **Key Concepts**: Watches the CRDs and standard Kubernetes resources. The Reconciler evaluates the actual state of the cluster against the desired policies defined in the CRDs, performing the correctness checks.

### 4. `internal/validator`
- **Responsibility**: The core rule engine.
- **Key Concepts**: Evaluates the cluster state against the rules defined in the CRDs. It identifies deviations, misconfigurations, and non-compliant resources safely.

### 5. `internal/webhook` (Future Scope)
- **Responsibility**: Admission controllers.
- **Key Concepts**: Validating or Mutating webhooks to reject non-compliant resources before they are admitted into the cluster.

## Flow of Execution

1. **Initialization**: The Operator pod starts, and the Manager begins watching registered resources.
2. **Reconciliation**: When a KubeOrthos CRD is created/updated, or watched resources change, the Reconciler is triggered.
3. **Validation**: The Reconciler delegates to the `validator` to check the resources against the policy.
4. **Status Update**: The Operator updates the `Status` subresource of the CRD to report violations or compliance, and emits Kubernetes Events.

## Development Principles

- **Testability**: Every component must be unit-testable. Use `envtest` for testing controller reconciliation logic.
- **Idempotency**: The Reconcile loop must be idempotent, safely re-running without unintended side effects.
- **Event-Driven**: Rely on Kubernetes watches and informers; avoid polling where possible.

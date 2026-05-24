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
- **Key Concepts**: Watches the CRDs and standard Kubernetes resources (e.g., `Node` objects). The `ClusterRuleReconciler` specifically watches `ClusterRule` objects and maps events from `Node` changes back to the `ClusterRule`s. It filters target nodes using `nodeSelector` and evaluates their specifications (Kubelet, Container Runtime, Kernel, OS, CPU Architecture), health status conditions (e.g., `MemoryPressure`), and allocatable hardware resource capacities against the configurations specified in the CRD. If `complianceLabel` is defined, it actively applies the label to compliant nodes and removes it from non-compliant nodes, updating the CRD's status with compliance conditions.

### 4. `internal/validator`
- **Responsibility**: The core rule engine.
- **Key Concepts**: Evaluates the cluster state against the rules defined in the CRDs. It identifies deviations, misconfigurations, and non-compliant resources safely.

### 5. `internal/webhook` (Future Scope)
- **Responsibility**: Admission controllers.
- **Key Concepts**: Validating or Mutating webhooks to reject non-compliant resources before they are admitted into the cluster.

## Flow of Execution

1. **Initialization**: The Operator pod starts, and the Manager begins watching registered resources. The `ClusterRuleReconciler` is registered to watch `ClusterRule` and `Node` kinds.
2. **Reconciliation Trigger**: When a `ClusterRule` is created/updated, or any watched `Node` changes, the Reconciler is triggered (via a Map function mapping Node events to all ClusterRules).
3. **Evaluation**: The Reconciler fetches all cluster `Node`s matching the `nodeSelector` (or all nodes if empty) and evaluates their specifications (Kubelet version, Container Runtime, Kernel version, OS image, CPU architecture), health status conditions, and allocatable hardware resource capacities against the rules specified in the `ClusterRule`.
4. **Status, Event, Active Labeling & Remediation Update**: 
   - If deviations are found, the Reconciler logs the non-compliance and emits warning events. If `complianceLabel` or `customLabels` are configured, it deletes these labels from the non-compliant node. If `action` is `Enforce` and the node is a worker node (not control plane), it actively cordons the node (`Unschedulable = true`) and adds a custom quarantine annotation. Control plane nodes are automatically skipped for safety.
   - If nodes are compliant, the Reconciler ensures the configured `complianceLabel` and `customLabels` are applied. If the node was previously cordoned/quarantined by KubeOrthos under `Action: Enforce`, it actively uncordons it and removes the quarantine annotation.
   - The Operator updates the `Status` subresource (specifically the `Conditions` array) of the `ClusterRule` to report the overall compliance state (True if compliant, False if deviations exist). If no nodes match the selector, it sets a distinct `NoMatchingNodes` reason.
   - All state modifications (labels and cordoning) are combined into a single transactional `Update` call per node to maximize performance.

## Development Principles

- **Testability**: Every component must be unit-testable. Use `envtest` for testing controller reconciliation logic.
- **Idempotency**: The Reconcile loop must be idempotent, safely re-running without unintended side effects.
- **Event-Driven**: Rely on Kubernetes watches and informers; avoid polling where possible.

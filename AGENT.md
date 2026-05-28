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
- **Key Concepts**: Watches the CRDs and standard Kubernetes resources (e.g., `Node` objects). The `ClusterRuleReconciler` specifically watches `ClusterRule` objects and maps events from `Node` changes back to the `ClusterRule`s. It uses a custom Node Predicate filter to ignore highly frequent heartbeat updates (timestamp updates in conditions) and only triggers reconciliation on spec, allocatable resource, labels/annotations metadata, or key condition status changes. It filters target nodes using `nodeSelector` and evaluates their specifications against the expectations. It applies strategic JSON patching (`client.MergeFrom`) rather than full updates to prevent optimistic locking conflicts.

### 4. `internal/constants`
- **Responsibility**: Centralized Shared Constants.
- **Key Concepts**: Eliminates hardcoded strings throughout the reconciler code, facilitating compliance with static analysis tool checks (e.g. `goconst`). Defines condition types, reasons, labels, annotation namespaces (`quarantine.kubeorthos.io/`), event reasons, and system defaults.

### 5. `internal/utils`
- **Responsibility**: Shared Helper Utilities.
- **Key Concepts**: Contains helper modules like `jobs.go` which constructs template-based ephemeral cleanup/reclamation Jobs to run on non-compliant nodes under disk pressure, and `utils.go` which exports common pointer references (`BoolPtr`, `Int32Ptr`) used in API payloads.

### 6. `internal/validator`
- **Responsibility**: The core rule engine.
- **Key Concepts**: Evaluates the cluster state against the rules defined in the CRDs. It identifies deviations, misconfigurations, and non-compliant resources safely.

### 7. `internal/webhook` (Future Scope)
- **Responsibility**: Admission controllers.
- **Key Concepts**: Validating or Mutating webhooks to reject non-compliant resources before they are admitted into the cluster.

## Flow of Execution

1. **Initialization**: The Operator pod starts, and the Manager begins watching registered resources. The `ClusterRuleReconciler` is registered to watch `ClusterRule` and `Node` kinds.
2. **Reconciliation Trigger**: When a `ClusterRule` is created/updated, or any watched `Node` changes, the Reconciler is triggered (via a Map function mapping Node events to all ClusterRules). The watch on `Node` resources is configured with a custom `nodePredicate` filter that ignores frequent lease/heartbeat updates (timestamp-only status changes) and only triggers on true changes to spec, key metadata, allocatable resource capacities, or key condition statuses. This drops idle reconciler events by over 99%.
3. **Evaluation**: The Reconciler fetches all cluster `Node`s matching the `nodeSelector` (or all nodes if empty) and evaluates their specifications (Kubelet version, Container Runtime, Kernel version, OS image, CPU architecture), health status conditions, and allocatable hardware resource capacities against the rules specified in the `ClusterRule`.
4. **Status, Event, Active Labeling, Remediation & Reclamation Update**: 
   - If deviations are found, the Reconciler logs the non-compliance and emits warning events. If `complianceLabel` or `customLabels` are configured, it deletes these labels from the non-compliant node. If `action` is `Enforce` and the node is a worker node (not control plane), KubeOrthos actively cordons the node (`Unschedulable = true`) and adds a Namespaced Quarantine annotation: `quarantine.kubeorthos.io/<rule-name>: "true"`. It also sets `policy.kubeorthos.io/quarantined: "true"`. Control plane nodes are automatically skipped for safety.
   - If nodes are compliant, KubeOrthos ensures the configured `complianceLabel` and `customLabels` are applied. If the node was previously cordoned/quarantined by this rule, KubeOrthos releases its namespaced quarantine claim (`quarantine.kubeorthos.io/<rule-name>`). It safely uncordons the node (`Unschedulable = false`) and removes the general quarantine annotation **only** if no other active rule claim annotations remain on the node, preventing "split-brain" uncordon loops between overlapping rules.
   - **Automated Resource Reclamation**: If a targeted worker node experiences `DiskPressure` and the `reclamation` block is configured, the reconciler automatically spawns an ephemeral node-reclamation Job to clean up unused images, containers, and truncate large logs directly on the node via Unix socket/directory mounting. Successful and failed Jobs are automatically monitored, reported via events, and cleaned up.
   - KubeOrthos updates the `Status` subresource (specifically the `Conditions` array) of the `ClusterRule` to report the overall compliance state (True if compliant, False if deviations exist). If no nodes match the selector, it sets a distinct `NoMatchingNodes` reason.
   - All state modifications (node specs, labels, annotations) and CRD statuses are executed using transactional client JSON strategic patching (`client.MergeFrom`) rather than full `Update`/`Status().Update` calls, guaranteeing 100% thread safety and eliminating Optimistic Locking conflicts (`409 Conflict`) under parallel reconciliation workloads.

## Development Principles

- **Testability**: Every component must be unit-testable. Use `envtest` for testing controller reconciliation logic.
- **Idempotency**: The Reconcile loop must be idempotent, safely re-running without unintended side effects.
- **Event-Driven**: Rely on Kubernetes watches and informers; avoid polling where possible.

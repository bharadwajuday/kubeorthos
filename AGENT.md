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
- **Key Concepts**: Contains the Go struct definitions for our CRDs. The primary CRD is `ClusterRule`, which is configured as a cluster-scoped (non-namespaced) resource. This protects the cluster's multi-tenancy model by restricting configuration to cluster administrators. It also enforces input parameter sanity (e.g., regex pattern validation on `LogSizeLimit`).

### 3. `internal/controller`
- **Responsibility**: The Reconcile loops and compliance logic.
- **Key Concepts**:
  - **NodeReconciler**: Reconciles `corev1.Node` resources. It fetches the node, lists all rules in the cache, audits the node against matching rules, stamps compliance tracking labels (`compliance.kubeorthos.io/<rule-name>: "true"|"false"`), and performs active enforcement (cordoning, quarantining, and disk pressure reclamation). This provides a scalable, node-centric $O(R)$ cache-lookup architecture per node event.
  - **ClusterRuleReconciler**: Reconciles `ClusterRule` resources to update overall status conditions and metrics. It watches `Nodes`, but uses a prefix-based, label-selective map watch that *only* enqueues rule reconciles when compliance tracking labels (`compliance.kubeorthos.io/`) or quarantine annotations change, preventing global fan-out thundering herds on node status updates.
  - **compliance.go**: Houses shared, package-level helper functions for node compliance auditing (`auditNodeCompliance`), cordoning (`reconcileCordoning`), labeling (`reconcileLabeling`), and reclamation (`reconcileReclamation`), which are invoked identically by both controllers.
  - Applies strategic JSON patching (`client.MergeFrom`) to prevent lock conflicts. Direct `pods` RBAC permissions are omitted from the manager role to enforce strict Least Privilege.

### 4. `internal/constants`
- **Responsibility**: Centralized Shared Constants.
- **Key Concepts**: Eliminates hardcoded strings throughout the reconciler code, facilitating compliance with static analysis tool checks (e.g. `goconst`). Defines condition types, reasons, labels, annotation namespaces (`quarantine.kubeorthos.io/`), event reasons, and system defaults.

### 5. `internal/utils`
- **Responsibility**: Shared Helper Utilities.
- **Key Concepts**: Contains helper modules like `jobs.go` which constructs template-based ephemeral cleanup/reclamation Jobs configured with a hardened security context (non-privileged mode, no privilege escalation, dropped capabilities, and `DAC_OVERRIDE` capability for secure log truncation). `utils.go` exports common pointer references (`BoolPtr`, `Int32Ptr`) used in API payloads.

### 6. `internal/validator`
- **Responsibility**: The core rule engine.
- **Key Concepts**: Evaluates the cluster state against the rules defined in the CRDs. It identifies deviations, misconfigurations, and non-compliant resources safely.

### 7. `internal/webhook`
- **Responsibility**: Admission controllers.
- **Key Concepts**: Contains the validating admission webhook (`clusterrule_webhook.go`) that intercepts `ClusterRule` creation and update requests. It enforces syntactic and semantic constraints, such as rejecting invalid/unparseable node selectors (via `metav1.LabelSelectorAsSelector` verification) and enforcing that `expectedNodeConfig` is never empty.

## Flow of Execution

1. **Initialization**: The Operator starts. The Manager configures client-side Cache Scoping for `corev1.Node` resources, matching only `kubernetes.io/os: linux` to limit memory and traffic. The `NodeReconciler` and `ClusterRuleReconciler` are registered.
2. **Bi-Directional Reconciliation Trigger**:
   - **NodeReconciler**: Watches `corev1.Node` updates (filtered by the heartbeat-ignoring `nodePredicate`). It also watches `ClusterRule` changes, mapping them to reconcile all nodes. When triggered, it fetches only the changing node and lists all rules in the cache.
   - **ClusterRuleReconciler**: Watches `ClusterRule` changes directly. It also watches `corev1.Node` updates, but uses a prefix-based, label-selective map watch that *only* enqueues rule reconciles when compliance tracking labels (`compliance.kubeorthos.io/`) or quarantine annotations change, preventing global fan-out thundering herds on node status updates.
3. **Evaluation & Enforcement (NodeReconciler)**:
   - Checks compliance using the shared auditing function.
   - Stamps compliance tracking labels: `compliance.kubeorthos.io/<rule-name>: "true"|"false"`.
   - If non-compliant and `action` is `Enforce` on a worker node, cordons the node and stamps rule-specific quarantine annotations: `quarantine.kubeorthos.io/<rule-name>: "true"`.
   - If compliant, uncordons the node and removes annotations (releasing claims), ensuring no other rules still have quarantine claims on the node.
   - **Automated Resource Reclamation**: If a targeted worker node experiences `DiskPressure` and the `reclamation` block is configured, it automatically spawns an ephemeral, non-privileged node-reclamation Job in the designated namespace (defaulting to `"default"`).
4. **Status & Metrics Updates (ClusterRuleReconciler)**:
   - When enqueued (via rule modification or compliance transitions), it lists the nodes targeted by the rule.
   - Computes overall compliance based on the compliance labels stamped on the nodes.
   - Patches the `Status` subresource (specifically the `Conditions` array) of the `ClusterRule` to report the overall compliance state (True if compliant, False if deviations exist).
   - Updates Prometheus metrics tracking evaluations, compliance, quarantines, and reclamation jobs.
5. **Conflict Prevention**:
   - All state modifications (node specs, labels, annotations) and CRD statuses are executed using transactional client JSON strategic patching (`client.MergeFrom`) rather than full `Update` calls, guaranteeing 100% thread safety and eliminating Optimistic Locking conflicts (`409 Conflict`) under parallel reconciliation workloads.

## Development Principles

- **Testability**: Every component must be unit-testable. Use `envtest` for testing controller reconciliation logic.
- **Idempotency**: The Reconcile loop must be idempotent, safely re-running without unintended side effects.
- **Event-Driven**: Rely on Kubernetes watches and informers; avoid polling where possible.

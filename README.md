# KubeOrthos

KubeOrthos is a Kubernetes Operator designed to ensure cluster-wide configuration correctness. 
It helps you validate, enforce, and maintain the desired state and best practices across your entire cluster configuration using Custom Resource Definitions (CRDs).

## Features
- **Bi-Directional Reconcile Loops (ClusterRule & Node Controllers)**: In addition to the primary `ClusterRule` controller, a dedicated `Node` controller reconciles `Node` resources, listing rules and evaluating node compliance in real-time. This guarantees immediate enforcement and compliance stamping upon node updates, join events, or status changes, with all evaluations using optimized watch predicates.
- **Targeted Auditing (`nodeSelector`)**: Audits nodes selectively using standard label selectors.
- **Rich System Metadata Validation**: Verifies node Kubelet, Container Runtime, Linux Kernel version, OS Image, and CPU Architecture.
- **Node Health & Resource Sanity Checks**: Audits node health status conditions (e.g., `MemoryPressure`) and checks minimum allocatable capacity requirements (CPU, Memory, Storage).
- **Scoped Multi-Rule Quarantining**: Safely manages concurrent policies. Non-compliant nodes are quarantined using namespaced labels (`quarantine.kubeorthos.io/<rule-name>`), and uncordoned only when *all* policies release their quarantine claims.
- **API Server & Event Filtering (Heartbeat Predicates)**: Drops reconciler resource usage by >99% on idle clusters by ignoring frequent, timestamp-only Node status updates (e.g., last heartbeat) and only triggering on genuine changes to specs, key metadata, allocatable capacity, or condition statuses.
- **Conflict-Resilient strategic JSON Patching**: Employs transactional client JSON strategic patching (`client.MergeFrom`) rather than full `Update` objects, ensuring 100% thread-safety and zero Optimistic Locking conflicts (`409 Conflict`) even when multiple rules reconcile in parallel.
- **Detailed Reconciliation Observability**: Outputs structured trace logs specifying exactly which rule expectations are evaluated for each node.
- **Validating Admission Webhook**: Performs real-time syntactic and semantic validation for `ClusterRule` resources at the API admission level. It uses `metav1.LabelSelectorAsSelector` to validate and reject unparseable/invalid node selectors, and enforces that `expectedNodeConfig` is never empty.

## Getting Started

### Prerequisites
- Go 1.26 or later
- Access to a Kubernetes v1.20+ cluster (e.g., kind, minikube)
- kubectl

### Installation

To run the operator locally against your configured Kubernetes cluster:
```bash
# 1. Install the Custom Resource Definitions (CRDs)
make install

# 2. Run the operator locally
make run
```

### Usage: ClusterRule

KubeOrthos allows you to define a cluster-scoped (non-namespaced) `ClusterRule` resource to audit your nodes and ensure they are running expected configurations like specific Kubelet and Container Runtime versions. Because it is cluster-scoped, only cluster-level administrators can configure policies, preventing privilege escalation from namespaced tenants.

1. Create a `ClusterRule` YAML file (e.g., `baseline.yaml`):
```yaml
apiVersion: audit.kubeorthos.io/v1alpha1
kind: ClusterRule
metadata:
  name: baseline-v1-34
spec:
  action: Audit
  nodeSelector:
    matchLabels:
      kubernetes.io/os: linux
  expectedNodeConfig:
    kubeletVersion: v1.34.0
    containerRuntime: containerd://1.7.29
    kernelVersion: 5.15.0-101-generic
    osImage: Ubuntu 22.04 LTS
    architecture: amd64
  expectedConditions:
    - type: Ready
      status: "True"
    - type: MemoryPressure
      status: "False"
  minimumResources:
    cpu: "2"
    memory: "2Gi"
    storage: "10Gi"
  complianceLabel:
    key: "policy.kubeorthos.io/compliant"
    value: "true"
  customLabels:
    environment: "production"
    tier: "frontend"
  reclamation:
    diskPressure:
      cleanImages: true
      cleanContainers: true
      cleanLogs: true
      logSizeLimit: "100Mi"
```

2. Apply the rule to your cluster:
```bash
kubectl apply -f baseline.yaml
```

3. The operator will audit only the nodes matching the label selector (or all nodes if `nodeSelector` is omitted). It will validate Kubelet, Container Runtime, Kernel version, OS image, architecture, health status conditions (e.g., MemoryPressure/DiskPressure), and hardware capacities (CPU, Memory, Storage). If `complianceLabel` or `customLabels` are defined in the CRD, it will automatically apply them to compliant targeted nodes and actively remove them from non-compliant targeted nodes.

4. **Active Enforcement (Cordoning & Scoped Quarantining)**: If `action` is set to `Enforce`, the operator actively quarantines non-compliant targeted worker nodes:
   - **Worker Nodes**: Actively cordoned (`Unschedulable = true`), annotated with `policy.kubeorthos.io/quarantined: "true"`, and stamped with a namespaced rule claim annotation: `quarantine.kubeorthos.io/<rule-name>: "true"`.
   - **Control Plane Nodes**: Automatically excluded from active cordoning to protect cluster control plane stability.
   - **Remediation & Reference-Counted Uncordoning**: Once a cordoned node becomes compliant, KubeOrthos releases its rule-specific quarantine claim (`quarantine.kubeorthos.io/<rule-name>`). The node is safely uncordoned and the general quarantined annotation removed **only** if no other active policies are still claiming quarantine annotations on that node. This prevents split-brain reconcile loops between overlapping rules.

5. **Automated Resource Reclamation**: If `reclamation` is configured and a targeted worker node experiences `DiskPressure`, KubeOrthos dynamically triggers a node-level cleanup Job to prune unused images, delete stopped containers, and truncate large logs to actively restore the node to health.
   - **Shell Injection (RCE) Prevention**: The `logSizeLimit` field uses a strict regex pattern validation marker (`^[0-9]+[kKmMgGtTpP]i?$`) at the Kubernetes API Admission level to block malicious shell characters or control sequences.
   - **Hardened Execution Context**: The reclamation Job container runs in a non-privileged context, blocks privilege escalation (`AllowPrivilegeEscalation: false`), drops all standard Linux capabilities, and mounts only the containerd socket and host logs directory with the narrow `DAC_OVERRIDE` capability (necessary to safely truncate root-owned log files on host volumes).

If any deviations are found, the operator will emit `Warning` events and set the `Compliant` status condition of the `ClusterRule` to `False`. If no nodes match the selector, the condition reason will be `NoMatchingNodes` with status `True`.

### Validating Webhook

KubeOrthos includes a Validating Admission Webhook that intercepts `ClusterRule` creation and update requests. The webhook enforces:
1. **Invalid Node Selectors**: If a `nodeSelector` is defined, it validates it using `metav1.LabelSelectorAsSelector`. Any invalid operator or expression is rejected before it is persisted in etcd.
2. **Empty Expected Node Configuration**: Enforces that `expectedNodeConfig` cannot be empty. At least one expected field (`kubeletVersion`, `containerRuntime`, `kernelVersion`, `osImage`, or `architecture`) must be specified.

## Project Structure

- `cmd/main.go`: Entry point for the Operator manager.
- `api/`: Custom Resource Definitions (CRDs) for configuring policies.
- `internal/controller/`: Reconcile loops to monitor and enforce configuration correctness.
  - `compliance.go`: Shared package-level helper functions for node compliance auditing and enforcement.

## AI Skills Files

This project utilizes specific "Skill Files" (e.g., `SKILL_UPDATE_CRD.md` and `AGENT.md`) that contain predefined rules and workflows for AI coding assistants to follow. When using AI agents on this repository, you can instruct them to read these files for context on best practices and required steps.

## Contributing

We welcome contributions! Please see the contributing guidelines (coming soon) for more information on how to get involved.

## License

*(License information will be added here.)*

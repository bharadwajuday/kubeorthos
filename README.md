# KubeOrthos

KubeOrthos is a Kubernetes Operator designed to ensure cluster-wide configuration correctness. 
It helps you validate, enforce, and maintain the desired state and best practices across your entire cluster configuration using Custom Resource Definitions (CRDs).

## Features
- **Targeted Auditing (`nodeSelector`)**: Audits nodes selectively using standard label selectors.
- **Rich System Metadata Validation**: Verifies node Kubelet, Container Runtime, Linux Kernel version, OS Image, and CPU Architecture.
- **Node Health & Resource Sanity Checks**: Audits node health status conditions (e.g., `MemoryPressure`) and checks minimum allocatable capacity requirements (CPU, Memory, Storage).
- **Detailed Reconciliation Observability**: Outputs structured trace logs specifying exactly which rule expectations are evaluated for each node.

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

KubeOrthos allows you to define a `ClusterRule` to audit your nodes and ensure they are running expected configurations like specific Kubelet and Container Runtime versions.

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

4. **Active Enforcement (Cordoning)**: If `action` is set to `Enforce`, the operator actively quarantines non-compliant targeted nodes:
   - **Worker Nodes**: Actively cordoned (`Unschedulable = true`) and annotated with `policy.kubeorthos.io/quarantined: "true"`.
   - **Control Plane Nodes**: Automatically excluded from active cordoning to protect cluster stability.
   - **Remediation**: Once a cordoned node becomes compliant, the operator automatically uncordons it and removes the quarantine annotation.

5. **Automated Resource Reclamation**: If `reclamation` is configured and a targeted worker node experiences `DiskPressure`, KubeOrthos dynamically triggers a node-level cleanup Job to prune unused images, delete stopped containers, and truncate large logs to actively restore the node to health.

If any deviations are found, the operator will emit `Warning` events and set the `Compliant` status condition of the `ClusterRule` to `False`. If no nodes match the selector, the condition reason will be `NoMatchingNodes` with status `True`.

## Project Structure

- `cmd/main.go`: Entry point for the Operator manager.
- `api/`: Custom Resource Definitions (CRDs) for configuring policies.
- `internal/controller/`: Reconcile loops to monitor and enforce configuration correctness.

## AI Skills Files

This project utilizes specific "Skill Files" (e.g., `SKILL_UPDATE_CRD.md` and `AGENT.md`) that contain predefined rules and workflows for AI coding assistants to follow. When using AI agents on this repository, you can instruct them to read these files for context on best practices and required steps.

## Contributing

We welcome contributions! Please see the contributing guidelines (coming soon) for more information on how to get involved.

## License

*(License information will be added here.)*

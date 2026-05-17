# Skill: Adding a New Field to a CRD

## Context
Use these instructions when asked to add a new configuration field (e.g., in `Spec` or `Status`) to an existing Custom Resource Definition, such as `ClusterRule`, in the KubeOrthos operator.

## Critical Rules
- **Rule 1:** NEVER manually edit files in `config/crd/bases/`. They are auto-generated.
- **Rule 2:** DO NOT delete or alter `// +kubebuilder:...` marker comments in the Go types files.
- **Rule 3:** ALWAYS update the corresponding sample YAML when adding a new `Spec` field.

## Step-by-Step Workflow

When asked to add a new field to a CRD, you must execute the following steps in order:

1. **Update Go Types:**
   - Open the appropriate types file (e.g., `api/v1alpha1/clusterrule_types.go`).
   - Add the new field to the `Spec` or `Status` struct.
   - Include appropriate JSON tags (e.g., `json:"newField,omitempty"`).
   - Add Kubebuilder validation markers as comments above the field if required (e.g., `// +kubebuilder:validation:Required`).

2. **Regenerate Code and Manifests:**
   - Run `make generate` to update the `zz_generated.deepcopy.go` methods.
   - Run `make manifests` to regenerate the CRD YAML in `config/crd/bases/`.

3. **Update Sample YAML (Required if Spec changed):**
   - Open `config/samples/audit_v1alpha1_clusterrule.yaml`.
   - Add the new field with a valid example value so users know how to use it.

4. **Update Controller Logic (If applicable):**
   - Open the controller file (e.g., `internal/controller/clusterrule_controller.go`).
   - Incorporate the new field into the `Reconcile` loop logic as requested.

5. **Verify Changes:**
   - Run `make test` to ensure unit tests still pass.
   - Inform the user that the field has been added and manifests regenerated.

## Common Pitfalls
- **Forgetting `make manifests`:** If you edit the Go structs but forget this step, the actual CRD installed on the cluster won't have the new field, and the Kubernetes API will reject custom resources using it.
- **Missing `omitempty`:** Be careful with omitting `omitempty` on struct fields unless it is strictly required, as it can cause issues with default serialization.

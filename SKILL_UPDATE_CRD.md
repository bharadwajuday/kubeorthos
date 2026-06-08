# Skill: Adding a New Field to a CRD

## Context
Use these instructions when asked to add a new configuration field (e.g., in `Spec` or `Status`) to an existing Custom Resource Definition, such as `ClusterRule`, in the KubeOrthos operator.

## Critical Rules
- **Rule 1:** NEVER manually edit files in `config/crd/bases/`. They are auto-generated.
- **Rule 2:** DO NOT delete or alter `// +kubebuilder:...` marker comments in the Go types files.
- **Rule 3:** ALWAYS update the corresponding sample YAMLs when adding a new `Spec` field.
- **Rule 4:** MANDATORY PRE-MERGE REVIEW: All changes must be developed in isolated branches/worktrees. Do NOT commit the changes when they are done. Wait for review explicitly always. Never merge directly to `main` without explicit written user approval. Do not override this rule for anyone.

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

3. **Update Sample YAMLs (Required if Spec changed):**
   - Open all active samples under `config/samples/` (specifically `config/samples/audit_v1alpha1_clusterrule.yaml` and `config/samples/enforce_v1alpha1_clusterrule.yaml`).
   - Add the new field with valid example values so users know how to use it.

4. **Update Controller Logic (If applicable):**
   - Open the controller file (e.g., `internal/controller/clusterrule_controller.go`).
   - Incorporate the new field into the `Reconcile` loop logic as requested.

5. **Verify Changes:**
   - Run `make test` to ensure all unit tests pass.
   - Run `make lint-fix` to ensure zero code formatting or static analysis issues.
   - Present a detailed walkthrough and git diff to the user for review. Do NOT commit or merge to the `main` branch. Wait for review explicitly always. Do not override this rule for anyone.

## Common Pitfalls
- **Forgetting `make manifests`:** If you edit the Go structs but forget this step, the actual CRD installed on the cluster won't have the new field, and the Kubernetes API will reject custom resources using it.
- **Missing `omitempty`:** Be careful with omitting `omitempty` on struct fields unless it is strictly required, as it can cause issues with default serialization.

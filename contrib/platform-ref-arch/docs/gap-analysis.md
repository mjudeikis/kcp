# Gap Analysis

Known gaps in kcp core and api-syncagent that affect this reference architecture, with proposed solutions.

## CRITICAL - Blocks the reference architecture

### Gap 1: Cross-CRD Reference Sync

**Component:** api-syncagent

**Current state:** `PublishedResource.spec.related` only supports ConfigMap and Secret as related resource kinds. There is no mechanism to declare that syncing one CRD requires also syncing a related CRD.

**Impact:** Most tools have mandatory cross-CRD references:
- ArgoCD: `Application.spec.project` -> `AppProject` (by name)
- Flux: `Kustomization.spec.sourceRef` -> `GitRepository` (by kind + name)
- Flux: `HelmRelease.spec.chart.spec.sourceRef` -> `HelmRepository` (by kind + name)
- Crossplane: `Claim` -> `ProviderConfig` (by reference)

Without cross-CRD sync, the synced resources fail reconciliation on the physical cluster because their referenced resources don't exist.

**Proposed solution:** Extend `PublishedResource.spec.related` to support arbitrary GVK references:

```yaml
spec:
  related:
    - identifier: app-project
      resource:
        group: argoproj.io
        version: v1alpha1
        resource: appprojects
      object:
        reference:
          path: ".spec.project"    # JSONPath to extract reference
      origin: kcp
```

**Alternative (simpler):** Add a "sync all instances of GVK for workspace" mode. Less precise but avoids complex reference parsing. Each PublishedResource would declare dependent GVKs that should always be fully synced for any workspace that has the parent resource:

```yaml
spec:
  dependsOn:
    - group: argoproj.io
      version: v1alpha1
      resource: appprojects
```

---

### Gap 2: Subresource Proxying (logs, exec, port-forward)

**Component:** kcp core + api-syncagent

**Current state:** kcp and api-syncagent handle standard CRUD operations only. No mechanism exists to proxy subresource requests (e.g., `pods/log`, `pods/exec`, `services/proxy`) from a kcp workspace to the physical cluster.

**Impact:** Argo Workflows is nearly unusable without Pod log access. Any tool that creates Pods and expects users to inspect them is affected. `kubectl logs`, `kubectl exec`, and `kubectl port-forward` don't work.

**Proposed solution (phased):**

Phase A - Read-only subresources (logs):
1. **kcp core:** Extend the virtual workspace / APIExport mechanism to support registering subresource proxy handlers. A new field in APIExport or APIResourceSchema would declare subresource routes and their proxy target.
2. **api-syncagent:** Expose a subresource proxy endpoint that accepts requests and forwards them to the physical cluster's API server using the agent's service account.

Phase B - Bidirectional subresources (exec, port-forward):
1. Requires WebSocket/SPDY proxying through kcp
2. Significantly more complex; may require a dedicated proxy component

**Minimal viable approach:** A sidecar or standalone proxy that users can access directly (bypassing kcp) with workspace-scoped authentication. Less elegant but unblocks the use case without kcp core changes.

---

## IMPORTANT - Significantly degrades experience

### Gap 3: Event Forwarding

**Component:** api-syncagent

**Current state:** Kubernetes Events generated on the physical cluster are not synced back to kcp workspaces.

**Impact:** `kubectl describe <resource>` and `kubectl get events` return nothing. This is the primary debugging workflow in Kubernetes. Users cannot understand why their resources are failing.

**Proposed solution:** Add event forwarding to api-syncagent:
1. Watch Events on the physical cluster filtered by `involvedObject` matching synced resources
2. Create corresponding Events in the kcp workspace
3. Apply short TTL (1 hour) and do not back-sync deletions
4. Filter to only Events related to objects synced from kcp (not all namespace events)

**Configuration in PublishedResource:**
```yaml
spec:
  events:
    enabled: true
    ttl: 1h
```

---

### Gap 4: Dynamic CRD-to-APIResourceSchema Automation

**Component:** New controller (contrib)

**Current state:** When Crossplane or KRO generate new CRDs from XRDs/ResourceGroups, there is no automated way to:
1. Create a corresponding APIResourceSchema in kcp
2. Update the APIExport to include the new resource
3. Create a PublishedResource for api-syncagent

This is a fully manual, error-prone process.

**Impact:** Defeats the self-service promise of Crossplane and KRO. Every new XRD or ResourceGroup requires 3+ manual object creations in kcp.

**Proposed solution:** A "CRD Discovery Controller" that runs alongside api-syncagent:

```
Physical Cluster                    kcp

CRD created by Crossplane  --->  CRD Discovery Controller  --->  APIResourceSchema
(from XRD)                        (watches CRDs matching           APIExport update
                                   configurable patterns)          PublishedResource
```

Implementation notes:
- kcp's existing `pkg/crdpuller/` package can extract OpenAPI schemas from CRDs
- Pattern matching via label selectors or group name patterns
- Idempotent: re-running against existing CRDs is a no-op
- Could be a standalone binary or a mode of api-syncagent

---

### Gap 5: Admission/Validation Forwarding

**Component:** api-syncagent

**Current state:** kcp supports URL-based admission webhooks in provider workspaces. But webhooks running on the physical cluster (e.g., Crossplane composition validation, ArgoCD admission) cannot intercept requests made in kcp.

**Impact:** Resources pass kcp-side validation but fail on the physical cluster. Users see a confusing delayed failure: the resource appears "created" in kcp but gets a sync error condition.

**Proposed solution:** Dry-run validation proxy in api-syncagent:
1. When a new resource is detected for sync, perform a `dry-run=All` create against the physical cluster's API server
2. If the dry-run fails (webhook rejection), set a condition on the kcp resource:
   ```yaml
   conditions:
     - type: ValidationFailed
       status: "True"
       message: "Physical cluster rejected: spec.chart.version is required"
   ```
3. Do not sync the resource until validation passes
4. Re-validate on spec updates

This is not synchronous rejection (the resource is created in kcp) but provides fast feedback.

---

### Gap 6: Status Aggregation / Normalization

**Component:** Contrib templates (works today)

**Current state:** Operator status structures are complex and tool-specific. ArgoCD Application status has deeply nested health/sync/resource trees. Crossplane Claim status reflects XR conditions.

**Impact:** Users see raw operator internals instead of a clean platform experience.

**Proposed solution:** Define standardized `PublishedResource.spec.mutation.status` templates per tool. This is achievable today with existing PublishedResource capabilities:

```yaml
spec:
  mutation:
    status:
      # Remove internal fields
      - delete:
          path: ".status.operationState"
      - delete:
          path: ".status.reconciledAt"
      # Keep: .status.health, .status.sync, .status.conditions
```

The reference architecture should ship opinionated status templates for each tool.

---

## NICE-TO-HAVE - Improves but not required

### Gap 7: Multi-Cluster Scheduling

**Component:** kcp core / new controller

**Current state:** api-syncagent runs per physical cluster. Workspace-to-cluster assignment is implicit (all agents watch all workspaces via the APIExport virtual workspace). No mechanism to route a workspace's resources to a specific cluster.

**Proposed solution:** Use kcp's `topology.kcp.io` Partition/PartitionSet to label workspaces with scheduling hints (region, tier, tool requirements). api-syncagent instances filter by workspace labels.

---

### Gap 8: Resource Quota per Workspace

**Component:** kcp core

**Current state:** kcp has `kubequota` for namespace-level quotas but no mechanism to limit synced resource counts per workspace on physical clusters.

**Proposed solution:** Leverage kcp's existing quota mechanism for APIExport-provided resources. Set ResourceQuotas in the provider workspace's maximal permission policy.

---

### Gap 9: Self-Service API Catalog

**Component:** New controller (contrib)

**Current state:** WorkspaceType `defaultAPIBindings` provides automatic binding. No catalog for users to discover and self-service bind to optional APIs.

**Proposed solution:** A catalog controller that reads APIExports from provider workspaces and generates a browsable catalog resource.

---

## Summary Matrix

| # | Gap | Component | Priority | Workaround Available |
|---|-----|-----------|----------|---------------------|
| 1 | Cross-CRD reference sync | api-syncagent | CRITICAL | Sync all instances of dependent types (wasteful) |
| 2 | Subresource proxying | kcp core + agent | CRITICAL | Direct cluster access for debugging (breaks model) |
| 3 | Event forwarding | api-syncagent | IMPORTANT | Users check physical cluster (breaks model) |
| 4 | CRD discovery automation | New controller | IMPORTANT | Manual creation (error-prone) |
| 5 | Admission forwarding | api-syncagent | IMPORTANT | Users see delayed sync errors |
| 6 | Status normalization | Contrib templates | IMPORTANT | Works today, needs templates |
| 7 | Multi-cluster scheduling | kcp core | NICE-TO-HAVE | Manual agent configuration |
| 8 | Resource quota | kcp core | NICE-TO-HAVE | Physical cluster ResourceQuota |
| 9 | Self-service catalog | New controller | NICE-TO-HAVE | Manual APIBinding creation |

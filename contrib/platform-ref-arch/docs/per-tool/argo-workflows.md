# Argo Workflows Integration

## Overview

Argo Workflows provides workflow automation on Kubernetes. Users define multi-step workflows as DAGs or sequences of containers. This is the most challenging integration because users expect to inspect Pod logs and exec into containers.

## Resources

| Resource | API Group | Scope | Notes |
|----------|-----------|-------|-------|
| Workflow | argoproj.io/v1alpha1 | Namespaced | Primary user-facing resource |
| WorkflowTemplate | argoproj.io/v1alpha1 | Namespaced | Reusable workflow definitions |
| CronWorkflow | argoproj.io/v1alpha1 | Namespaced | Scheduled workflows |
| ClusterWorkflowTemplate | argoproj.io/v1alpha1 | Cluster | Shared templates (platform team) |

## Cross-CRD References

- `Workflow.spec.workflowTemplateRef` -> `WorkflowTemplate` (by name)
- `CronWorkflow.spec.workflowSpec.workflowTemplateRef` -> `WorkflowTemplate`
- Workflow steps reference `ConfigMap` and `Secret` for parameters and artifacts

All workflow-related CRDs sync independently via their own PublishedResources. WorkflowTemplate must exist before a Workflow referencing it can run.

## Critical Gap: Pod Logs and Exec

Argo Workflows creates Pods for each workflow step. Users expect to:
- View Pod logs: `kubectl logs <workflow-pod>`
- Stream logs in real-time: `kubectl logs -f <workflow-pod>`
- Exec into containers: `kubectl exec -it <workflow-pod> -- /bin/sh`
- View Pod status: `kubectl get pods`

**This requires subresource proxying (Gap 2 in gap-analysis.md).**

Without this, Argo Workflows is limited to:
- Creating and monitoring Workflows via status
- Viewing workflow step outcomes (success/failure)
- But NOT inspecting step execution details

### Workaround Options

1. **Argo Workflows UI proxy:** Expose the Argo Workflows web UI through an ingress with workspace-scoped authentication. Users access logs and artifacts through the UI instead of kubectl.
2. **Artifact storage:** Configure Argo Workflows to store step logs as artifacts (S3, GCS, MinIO). Expose an artifact browser in kcp.
3. **Log aggregation:** Forward Pod logs to a centralized logging system (Loki, Elasticsearch) with workspace-scoped queries.

## Permission Claims

The Argo Workflows APIExport needs:
- `secrets` (get, list, watch) - workflow parameters, artifact credentials
- `configmaps` (get, list, watch) - workflow parameters

## Status Fields

Argo Workflow status includes:
- `.status.phase` - Running, Succeeded, Failed, Error
- `.status.startedAt` / `.status.finishedAt` - timing
- `.status.nodes` - detailed per-step status (large, consider trimming)
- `.status.conditions` - standard conditions
- `.status.outputs` - workflow outputs (parameters, artifacts)

Recommended status mutations:
```yaml
mutation:
  status:
    # Trim large internal fields
    - delete:
        path: ".status.nodes.*.inputs"
    - delete:
        path: ".status.nodes.*.outputs.artifacts"
    - delete:
        path: ".status.compressedNodes"
```

## Physical Cluster Setup

```bash
# Install Argo Workflows
kubectl create namespace argo
kubectl apply -n argo -f https://github.com/argoproj/argo-workflows/releases/latest/download/install.yaml

# Configure for multi-tenant namespace mode
# Set ARGO_NAMESPACE="" to watch all namespaces
```

## User Experience

```bash
# In kcp workspace root:tenants:team-alpha
kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: hello-world
  namespace: default
spec:
  entrypoint: hello
  templates:
    - name: hello
      container:
        image: alpine:3.18
        command: [echo, "Hello from kcp workspace!"]
EOF

kubectl get workflows
# NAME          STATUS      AGE
# hello-world   Succeeded   1m

# View step status (synced from physical cluster)
kubectl get workflow hello-world -o jsonpath='{.status.phase}'
# Succeeded

# Pod logs - REQUIRES Gap 2 (subresource proxying)
# kubectl logs hello-world-hello-123456  # Does not work today
```

## Implementation Phases

| Phase | Capability | Status |
|-------|-----------|--------|
| 1 | Workflow CRUD + status sync | Works with current kcp |
| 1 | WorkflowTemplate + CronWorkflow sync | Works with current kcp |
| 2 | Event forwarding for step failures | Requires Gap 3 |
| 4 | Pod logs/exec through kcp | Requires Gap 2 (subresource proxy) |

## Known Limitations

1. **No Pod logs/exec** without subresource proxying - the most significant limitation
2. **Artifact storage** must be pre-configured by platform team (S3 bucket, credentials)
3. **Argo CLI** (`argo logs`, `argo watch`) requires direct cluster access
4. **Resource limits:** Workflows can consume significant cluster resources. Platform team should set ResourceQuotas on tenant namespaces.

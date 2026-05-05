# Platform Reference Architecture

A reference architecture for building a multi-tenant platform on kcp where users get workspace-based access to Kubernetes capabilities (GitOps, infrastructure provisioning, workflow orchestration) without direct cluster access.

## Overview

```
                         kcp Server
                 ========================

root
 +-- platform-providers              (universal)
 |    +-- argocd-provider            APIExport: argocd.argoproj.io
 |    +-- argo-workflows-provider    APIExport: workflows.argoproj.io
 |    +-- flux-provider              APIExport: flux.toolkit.fluxcd.io
 |    +-- crossplane-provider        APIExport: crossplane.io
 |    +-- kro-provider               APIExport: kro.run
 |
 +-- tenants                         (organization)
      +-- team-alpha                 (application WorkspaceType)
      |    APIBindings: argocd, flux
      +-- team-beta
           APIBindings: crossplane, kro, argo-workflows

              Physical Clusters (never exposed to users)
              ==========================================
 cluster-a:  ArgoCD + Flux + api-syncagent
 cluster-b:  Crossplane + KRO + api-syncagent
 cluster-c:  Argo Workflows + api-syncagent
```

## How It Works

1. **Platform team** deploys kcp, physical clusters, and operators (ArgoCD, Flux, Crossplane, KRO, Argo Workflows)
2. **Platform team** creates provider workspaces in kcp with APIExports for each tool
3. **Platform team** deploys api-syncagent on each physical cluster with PublishedResource configs
4. **Application teams** get `application`-type workspaces with auto-provisioned APIBindings
5. **Users** create resources (e.g., ArgoCD Application) in their kcp workspace
6. **api-syncagent** syncs resources to the physical cluster where the operator reconciles them
7. **Status** flows back through api-syncagent to the user's kcp workspace

Users never see or access the underlying clusters. They interact with a Kubernetes-like API surface in their workspace.

## Data Flow

```
User creates ArgoCD Application in kcp workspace
        |
        v
kcp stores resource (via APIBinding -> APIExport)
        |
        v
api-syncagent watches APIExport virtual workspace
        |
        v
api-syncagent creates Application on physical cluster
        |
        v
ArgoCD controller reconciles Application
        |
        v
ArgoCD updates Application.status
        |
        v
api-syncagent syncs status back to kcp workspace
        |
        v
User sees status in their kcp workspace
```

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Per-workspace namespace isolation on physical clusters (`ws-<hash>`) | Strong tenant isolation using existing K8s RBAC/NetworkPolicy/ResourceQuota |
| One APIExport per tool (not per version) | Simplifies consumer binding; bind once, get all APIs |
| Provider workspaces under shared parent (`root:platform-providers`) | Centralized platform management, predictable paths |
| `application` WorkspaceType with `defaultAPIBindings` + `Maintain` lifecycle | Auto-provision on workspace creation, day-2 updates propagate |
| WorkspaceType initializer for bootstrapping | RBAC, namespaces, welcome resources created automatically |

## Directory Structure

```
platform-ref-arch/
  manifests/
    kcp/
      workspace-types/
        application.yaml          # WorkspaceType definition
      providers/
        argocd/                   # APIResourceSchemas + APIExport
        flux/
        crossplane/
        kro/
        argo-workflows/
    physical-cluster/
      argocd/                     # PublishedResource configs
      flux/
      crossplane/
      kro/
      argo-workflows/
  docs/
    architecture.md               # Detailed architecture
    gap-analysis.md               # Known gaps and proposed solutions
    per-tool/                     # Per-tool integration details
  examples/                       # End-to-end usage examples
```

## Supported Tools

| Tool | Status | Notes |
|------|--------|-------|
| ArgoCD | Phase 1 | Application, AppProject, ApplicationSet |
| Flux | Phase 1 | GitRepository, Kustomization, HelmRelease, HelmRepository |
| Crossplane | Phase 1 | Claims (user-facing XRDs) |
| KRO | Phase 1 | ResourceGroup + generated instance CRDs |
| Argo Workflows | Phase 4 | Requires subresource proxying for Pod logs/exec |

## Prerequisites

- A running kcp instance (see `contrib/production/` for deployment options)
- One or more physical Kubernetes clusters with operators installed
- [api-syncagent](https://github.com/kcp-dev/api-syncagent) deployed on each physical cluster
- `kubectl` with the `ws` plugin

## Quick Start

1. Create the provider workspaces and apply APIExports:
   ```bash
   kubectl ws root
   kubectl ws create platform-providers --enter
   # Apply provider manifests for your chosen tools
   kubectl ws create argocd-provider --enter
   kubectl apply -f manifests/kcp/providers/argocd/
   ```

2. Apply the `application` WorkspaceType:
   ```bash
   kubectl ws root:tenants
   kubectl apply -f manifests/kcp/workspace-types/application.yaml
   ```

3. Deploy api-syncagent with PublishedResource configs on each physical cluster:
   ```bash
   # On the physical cluster running ArgoCD
   kubectl apply -f manifests/physical-cluster/argocd/
   ```

4. Create a tenant workspace:
   ```bash
   kubectl ws root:tenants
   kubectl ws create team-alpha --type application --enter
   # APIBindings are auto-created by the WorkspaceType
   ```

5. Use the tools from the workspace:
   ```bash
   kubectl apply -f examples/argocd-application.yaml
   kubectl get applications  # Status synced back from physical cluster
   ```

## Known Gaps

See [docs/gap-analysis.md](docs/gap-analysis.md) for a detailed analysis of gaps in kcp core and api-syncagent that affect this architecture. Key gaps:

- **Cross-CRD reference sync** (api-syncagent `related` limited to ConfigMap/Secret)
- **Subresource proxying** (no Pod logs/exec through kcp)
- **Event forwarding** (no Events from physical cluster in kcp workspace)
- **Dynamic CRD discovery** (manual APIResourceSchema creation for Crossplane/KRO)

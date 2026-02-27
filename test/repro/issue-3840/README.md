# Repro: permissionClaim on tenancy.kcp.io returns 403 (#3840)

Bug report: https://github.com/kcp-dev/kcp/issues/3840

## Summary

When an APIExport includes a `permissionClaim` on `tenancy.kcp.io/workspaces`,
and the consumer accepts that claim in an APIBinding, the provider's ServiceAccount
(which has RBAC for `apiexports/content`) still receives a **403 Forbidden** when
it tries to list workspaces via the APIExport virtual workspace.

## Setup

- kcp running locally (e.g. `./bin/kcp start`)
- `kubectl` configured to talk to the kcp root workspace
- `KUBECONFIG` points to the kcp admin kubeconfig

## Manual Steps

```bash
export KCP_URL=https://localhost:6443
export KUBECONFIG=<path-to-kcp-admin.kubeconfig>

# 1. Create provider workspace
kubectl kcp ws create provider --enter
# (note the provider cluster ID from workspace URL)

# 2. Install cowboys CRD (used as the exported resource)
kubectl apply -f ../../fixtures/wildwest/wildwest.dev_cowboys.yaml

# 3. Create APIResourceSchema for cowboys
kubectl apply -f apiresourceschema_cowboys.yaml

# 4. Create APIExport with permissionClaim to tenancy.kcp.io/workspaces
#    (identity hash must be filled from the root tenancy.kcp.io APIExport)
TENANCY_HASH=$(kubectl --context root get apiexport tenancy.kcp.io -o jsonpath='{.status.identityHash}')
sed "s/TENANCY_IDENTITY_HASH/$TENANCY_HASH/" apiexport.yaml | kubectl apply -f -

# 5. Create provider ServiceAccount + RBAC
kubectl apply -f provider-rbac.yaml

# 6. Create consumer workspace
kubectl kcp ws root
kubectl kcp ws create consumer --enter

# 7. Create APIBinding in consumer
# The permissionClaim for tenancy.kcp.io/workspaces requires the same tenancy
# identity hash that was used in apiexport.yaml (fetched in step 4).
sed "s/TENANCY_IDENTITY_HASH/$TENANCY_HASH/" apibinding.yaml | kubectl apply -f -

# 8. Get SA token from provider workspace
kubectl kcp ws :root:provider
SA_TOKEN=$(kubectl create token provider-sa -n repro-ns)

# 9. Get provider cluster ID
PROVIDER_CLUSTER=$(kubectl get workspace provider -o jsonpath='{.spec.cluster}' --context root)

# 10. Try to access workspaces via virtual workspace (FAILS with 403)
curl -k -XGET "$KCP_URL/services/apiexport/$PROVIDER_CLUSTER/wildwest.dev/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces" \
  --header "Authorization: Bearer $SA_TOKEN"

# Expected: list of workspaces
# Actual:   403 "workspaces.tenancy.kcp.io is forbidden: ... access denied"
```

## Automated Repro

Run the integration test (requires a running kcp instance):

```bash
go test -v -run TestReproIssue3840 -tags e2e ./test/repro/issue-3840/...
```

Or with the full e2e test suite setup:

```bash
go test -v -run TestReproIssue3840 -tags e2e -count=1 ./test/repro/issue-3840/...
```

The test **asserts a 403 response**, confirming the bug. Once the bug is fixed,
the assertion should be changed to expect success (2xx).

## Root Cause Hypothesis

The virtual workspace authorizer checks RBAC for the SA when it tries to
access a claimed resource (workspaces), but the claim RBAC is not correctly
propagated/applied when the requester is a ServiceAccount (rather than a
regular user). The SA has explicit RBAC for `apiexports/content` which should
grant access to all claimed resources via the virtual workspace.

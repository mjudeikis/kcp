/*
Copyright 2025 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package issue3840 contains a minimal reproducible test case for
// https://github.com/kcp-dev/kcp/issues/3840
//
// Bug: when a ServiceAccount token is used to access a permissionClaim on
// tenancy.kcp.io/workspaces via an APIExport virtual workspace, the request
// returns 403 Forbidden even though:
//  1. The APIExport declares a permissionClaim for tenancy.kcp.io/workspaces
//  2. The consumer's APIBinding accepts that claim
//  3. The SA has ClusterRole access to apiexports/content
package issue3840

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/tenancy"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// workspacesGVR is the GVR for tenancy.kcp.io/v1alpha1 workspaces.
var workspacesGVR = schema.GroupVersionResource{
	Group:    tenancy.GroupName,
	Version:  "v1alpha1",
	Resource: "workspaces",
}

// TestReproIssue3840 reproduces the bug where a ServiceAccount accessing
// tenancy.kcp.io/workspaces via an APIExport virtual workspace receives 403
// even though the permissionClaim is accepted and the SA has apiexports/content access.
//
// Expected (once bug is fixed): 2xx response listing workspaces
// Actual (current behavior):    403 Forbidden
func TestReproIssue3840(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	// Create clients
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client")

	// --- Step 1: Create org, provider, and consumer workspaces ---
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(),
		kcptesting.WithType(core.RootCluster.Path(), "organization"),
		kcptesting.WithNamePrefix("repro-3840"),
	)

	t.Logf("Creating provider workspace under %s", orgPath)
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath,
		kcptesting.WithNamePrefix("provider"),
	)

	t.Logf("Creating consumer workspace under %s", orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath,
		kcptesting.WithNamePrefix("consumer"),
	)

	// --- Step 2: Fetch tenancy.kcp.io identity hash from root ---
	// The permissionClaim for tenancy.kcp.io/workspaces must reference the
	// identity hash of the built-in tenancy.kcp.io APIExport in root.
	t.Logf("Fetching tenancy.kcp.io identity hash from root workspace")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(
			t.Context(), "tenancy.kcp.io", metav1.GetOptions{},
		)
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "tenancy.kcp.io APIExport never became valid")

	tenancyExport, err := kcpClusterClient.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(
		t.Context(), "tenancy.kcp.io", metav1.GetOptions{},
	)
	require.NoError(t, err)
	tenancyIdentityHash := tenancyExport.Status.IdentityHash
	require.NotEmpty(t, tenancyIdentityHash, "tenancy.kcp.io identity hash must not be empty")
	t.Logf("tenancy.kcp.io identity hash: %s", tenancyIdentityHash)

	// --- Step 3: Install APIResourceSchema for cowboys in provider ---
	t.Logf("Installing APIResourceSchema for cowboys into provider workspace %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(
		memory.NewMemCacheClient(kubeClusterClient.Cluster(providerPath).Discovery()),
	)
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil,
		"apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err, "failed to create APIResourceSchema")

	// --- Step 4: Create APIExport in provider with permissionClaim for tenancy.kcp.io/workspaces ---
	t.Logf("Creating APIExport wildwest.dev in provider workspace %q", providerPath)
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wildwest.dev",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "events"},
					Verbs:         []string{"list", "watch", "get"},
				},
				{
					// Claim workspaces from tenancy.kcp.io — this is the problematic claim.
					GroupResource: apisv1alpha2.GroupResource{Group: tenancy.GroupName, Resource: "workspaces"},
					IdentityHash:  tenancyIdentityHash,
					Verbs:         []string{"get", "list", "watch"},
				},
			},
		},
	}
	apiExport, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(
		t.Context(), apiExport, metav1.CreateOptions{},
	)
	require.NoError(t, err, "failed to create APIExport")

	// Wait for APIExport identity to be valid
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(
			t.Context(), "wildwest.dev", metav1.GetOptions{},
		)
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "wildwest.dev APIExport never became valid")

	// --- Step 5: Create namespace + ServiceAccount + RBAC in provider ---
	t.Logf("Creating namespace repro-ns in provider workspace %q", providerPath)
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "repro-ns"},
	}
	_, err = kubeClusterClient.Cluster(providerPath).CoreV1().Namespaces().Create(
		t.Context(), ns, metav1.CreateOptions{},
	)
	require.NoError(t, err, "failed to create namespace")

	t.Logf("Creating ServiceAccount provider-sa in provider workspace")
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "provider-sa",
			Namespace: "repro-ns",
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).CoreV1().ServiceAccounts("repro-ns").Create(
		t.Context(), sa, metav1.CreateOptions{},
	)
	require.NoError(t, err, "failed to create ServiceAccount")

	t.Logf("Creating ClusterRole/ClusterRoleBinding for provider-sa in provider workspace")
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-sa-role"},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apiexportendpointslices"},
				Verbs:     []string{"list", "watch", "get"},
			},
			{
				// This is the critical permission: apiexports/content grants access
				// to all claimed resources via the virtual workspace.
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports/content"},
				ResourceNames: []string{"wildwest.dev"},
				Verbs:         []string{"*"},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(
		t.Context(), cr, metav1.CreateOptions{},
	)
	require.NoError(t, err, "failed to create ClusterRole")

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-sa-rolebinding"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "provider-sa-role",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "provider-sa",
				Namespace: "repro-ns",
			},
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(
		t.Context(), crb, metav1.CreateOptions{},
	)
	require.NoError(t, err, "failed to create ClusterRoleBinding")

	// --- Step 6: Create APIBinding in consumer accepting the tenancy claim ---
	t.Logf("Creating APIBinding in consumer workspace %q", consumerPath)
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wildwest.dev",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: "wildwest.dev",
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "events"},
							Verbs:         []string{"list", "watch", "get"},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
					},
					State: apisv1alpha2.ClaimAccepted,
				},
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Group:    tenancy.GroupName,
								Resource: "workspaces",
							},
							IdentityHash: tenancyIdentityHash,
							Verbs:        []string{"get", "list", "watch"},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
					},
					State: apisv1alpha2.ClaimAccepted,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(
			t.Context(), apiBinding, metav1.CreateOptions{},
		)
		if err != nil {
			return false, fmt.Sprintf("error creating APIBinding: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create APIBinding in consumer workspace")

	t.Logf("Waiting for APIBinding to reach InitialBindingCompleted")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(
			t.Context(), "wildwest.dev", metav1.GetOptions{},
		)
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted), "APIBinding never completed initial binding")

	// --- Step 7: Get VW URL for the APIExport ---
	t.Logf("Waiting for APIExport virtual workspace URL to be available for consumer workspace")
	vwCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		endpointSlice, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(
			t.Context(), "wildwest.dev", metav1.GetOptions{},
		)
		if err != nil {
			return false, fmt.Sprintf("error getting APIExportEndpointSlice: %v", err)
		}
		urls := framework.ExportVirtualWorkspaceURLs(endpointSlice)
		var found bool
		vwCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClusterClient, consumerWorkspace, urls)
		if err != nil {
			return false, fmt.Sprintf("error resolving VW URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for VW URL, available endpoints: %v", urls)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "APIExport VW URL never became available")
	t.Logf("Virtual workspace URL: %s", vwCfg.Host)

	// --- Step 8: Create a ServiceAccount token ---
	t.Logf("Waiting for ServiceAccount to be ready")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClusterClient.Cluster(providerPath).CoreV1().ServiceAccounts("repro-ns").Get(
			t.Context(), "provider-sa", metav1.GetOptions{},
		)
		return err == nil, fmt.Sprintf("waiting for SA: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "ServiceAccount never became ready")

	t.Logf("Creating token for provider-sa")
	var saToken string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		tokenReq, err := kubeClusterClient.Cluster(providerPath).CoreV1().ServiceAccounts("repro-ns").CreateToken(
			t.Context(), "provider-sa",
			&authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					Audiences:         []string{"https://kcp.default.svc"},
					ExpirationSeconds: ptr.To[int64](3600),
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil {
			return false, fmt.Sprintf("error creating token: %v", err)
		}
		saToken = tokenReq.Status.Token
		return saToken != "", "token is empty"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create SA token")
	t.Logf("SA token obtained (length=%d)", len(saToken))

	// --- Step 9: Try to list workspaces via VW using the SA token ---
	// The SA has apiexports/content RBAC and the consumer has accepted the
	// tenancy.kcp.io/workspaces permissionClaim. This SHOULD work but returns 403.
	saVWCfg := rest.CopyConfig(vwCfg)
	saVWCfg.BearerToken = saToken
	saVWCfg.CertData = nil
	saVWCfg.KeyData = nil

	saDynamicClient, err := kcpdynamic.NewForConfig(saVWCfg)
	require.NoError(t, err, "failed to create dynamic client with SA token")

	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	t.Logf("Attempting to list workspaces via virtual workspace using SA token (expects 403 - this is the bug)")
	t.Logf("  Virtual workspace URL: %s", saVWCfg.Host)
	t.Logf("  Consumer cluster: %s", consumerClusterName)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	_, err = saDynamicClient.Cluster(consumerClusterName.Path()).Resource(workspacesGVR).List(ctx, metav1.ListOptions{})

	// BUG CONFIRMED: The SA token with apiexports/content RBAC receives 403
	// when trying to list workspaces claimed via permissionClaim.
	//
	// Once the bug is fixed, this should be changed to:
	//   require.NoError(t, err, "expected to list workspaces successfully")
	require.True(t, apierrors.IsForbidden(err),
		"expected 403 Forbidden when listing workspaces via VW with SA token (bug #3840), got: %v", err)

	t.Logf("BUG REPRODUCED: SA token received 403 when listing tenancy.kcp.io/workspaces via VW: %v", err)
	t.Logf("Issue: https://github.com/kcp-dev/kcp/issues/3840")
}

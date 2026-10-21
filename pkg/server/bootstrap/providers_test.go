/*
Copyright 2026 The kcp Authors.

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

package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	kcpfake "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"

	configshard "github.com/kcp-dev/kcp/config/shard"
)

func fixed(hash string) Provider {
	return func(context.Context, string) (string, error) { return hash, nil }
}

func failing(msg string) Provider {
	return func(context.Context, string) (string, error) { return "", errors.New(msg) }
}

func TestChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	hash, err := Chain(failing("first"), nil, fixed("a"), fixed("b"))(ctx, "tenancy.kcp.io")
	require.NoError(t, err)
	require.Equal(t, "a", hash, "the first provider with an answer wins")

	_, err = Chain(failing("first"), failing("second"))(ctx, "tenancy.kcp.io")
	require.ErrorContains(t, err, "first")
	require.ErrorContains(t, err, "second")

	_, err = Chain()(ctx, "tenancy.kcp.io")
	require.ErrorIs(t, err, ErrUnknown)
}

func apiIdentity(in logicalcluster.Name, name, cluster, export string, core bool) *adminv1alpha1.APIIdentity {
	return &adminv1alpha1.APIIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: in.String()},
		},
		Status: adminv1alpha1.APIIdentityStatus{
			Owner: &adminv1alpha1.ExportIdentityReference{Cluster: cluster, Export: export},
			Core:  core,
		},
	}
}

func TestAPIIdentityProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// the registry entries live in system:shard on the cache server and are
	// served under any cluster path by the Admin workspace, root included.
	objects := func(in logicalcluster.Name) []runtime.Object {
		return []runtime.Object{
			apiIdentity(in, "system-tenancy.kcp.io", "root", "tenancy.kcp.io", true),
			apiIdentity(in, "ws-1-abc", "ws-1", "tenancy.kcp.io", false),
			apiIdentity(in, "root-def", "root", "topology.kcp.io", false),
		}
	}
	for name, provider := range map[string]Provider{
		"cache server":    APIIdentityProvider(kcpfake.NewClientset(objects(configshard.SystemShardCluster)...)),
		"admin workspace": AdminWorkspaceProvider(kcpfake.NewClientset(objects(core.RootCluster)...)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			hash, err := provider(ctx, "tenancy.kcp.io")
			require.NoError(t, err)
			require.Equal(t, "system-tenancy.kcp.io", hash, "only the core identity owned by the root export counts")

			_, err = provider(ctx, "topology.kcp.io")
			require.ErrorIs(t, err, ErrUnknown, "an identity not marked core is ignored")

			_, err = provider(ctx, "shards.core.kcp.io")
			require.ErrorIs(t, err, ErrUnknown)
		})
	}
}

func TestIdentitiesResolve(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	groups := map[string]string{"tenancy.kcp.io": "tenancy.kcp.io"}
	grs := map[schema.GroupResource]string{
		{Group: "core.kcp.io", Resource: "shards"}:                        "shards.core.kcp.io",
		{Group: "migration.kcp.io", Resource: "logicalclustermigrations"}: "migration.kcp.io",
		{Group: "migration.kcp.io", Resource: "apiexportstoragerekeys"}:   "migration.kcp.io",
	}
	ids := NewIdentities(groups, grs)
	require.Equal(t, []string{"migration.kcp.io", "shards.core.kcp.io", "tenancy.kcp.io"}, ids.ExportNames())

	partial := func(_ context.Context, name string) (string, error) {
		if name == "tenancy.kcp.io" {
			return "t", nil
		}
		return "", ErrUnknown
	}
	require.Error(t, ids.Resolve(ctx, partial))

	rest := func(_ context.Context, name string) (string, error) {
		if name == "tenancy.kcp.io" {
			return "changed", nil
		}
		return name, nil
	}
	require.NoError(t, ids.Resolve(ctx, rest))

	got, _, err := decorateWildcardPathsWithResourceIdentities("/clusters/*/apis/migration.kcp.io/v1alpha1/logicalclustermigrations", ids)
	require.NoError(t, err)
	require.Equal(t, "/clusters/*/apis/migration.kcp.io/v1alpha1/logicalclustermigrations:migration.kcp.io", got)
	got, _, err = decorateWildcardPathsWithResourceIdentities("/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces/root", ids)
	require.NoError(t, err)
	require.Equal(t, "/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:t/root", got, "Resolve never replaces a resolved identity")
}

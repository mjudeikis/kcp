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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/identitycache"
)

// Provider returns the identity of the named core APIExport. It returns an
// error wrapping ErrUnknown when it has no identity for the export, so that a
// Chain can move on to the next provider.
type Provider func(ctx context.Context, exportName string) (string, error)

// ErrUnknown is returned (wrapped) by a Provider that has no identity for
// the export.
var ErrUnknown = errors.New("identity unknown")

// Chain tries the providers in order and returns the first identity found.
// Every provider is tried; the errors of all of them are reported when none
// has an answer. nil providers are skipped.
func Chain(providers ...Provider) Provider {
	return func(ctx context.Context, exportName string) (string, error) {
		var errs []error
		for _, p := range providers {
			if p == nil {
				continue
			}
			hash, err := p(ctx, exportName)
			if err == nil && hash != "" {
				return hash, nil
			}
			if err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) == 0 {
			return "", ErrUnknown
		}
		return "", utilerrors.NewAggregate(errs)
	}
}

// RootAPIExportProvider reads the identity from the APIExport in the root
// workspace, through the given config: the loopback config on the root
// shard, or a config for the root shard on the other shards (legacy).
func RootAPIExportProvider(config *rest.Config) Provider {
	return func(ctx context.Context, exportName string) (string, error) {
		client, err := kcpclientset.NewForConfig(config)
		if err != nil {
			return "", err
		}
		apiExport, err := client.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(ctx, exportName, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("root workspace: %w", err)
		}
		if apiExport.Status.IdentityHash == "" {
			return "", fmt.Errorf("root workspace: APIExport %s has no identity yet: %w", exportName, ErrUnknown)
		}
		return apiExport.Status.IdentityHash, nil
	}
}

// APIIdentityProvider reads the identity of a core export from the
// APIIdentity registry on the cache server (enhancements KEP 0006): the
// registry lists every identity with its owner, and marks the identities of
// the core exports in the root workspace.
func APIIdentityProvider(cacheKcpClusterClient kcpclientset.ClusterInterface) Provider {
	return func(ctx context.Context, exportName string) (string, error) {
		identities, err := cacheKcpClusterClient.AdminV1alpha1().APIIdentities().List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", fmt.Errorf("cache server: APIIdentities: %w", err)
		}
		if hash, found := coreIdentityFrom(identities.Items, exportName); found {
			return hash, nil
		}
		return "", fmt.Errorf("cache server has no APIIdentity for %s: %w", exportName, ErrUnknown)
	}
}

// AdminWorkspaceProvider reads the identity of a core export from the
// APIIdentity registry as served through the Admin workspace of the shards,
// which the front-proxy reaches through its peer kubeconfigs. It needs no
// cache server access and no root shard.
func AdminWorkspaceProvider(adminClusterClient kcpclientset.ClusterInterface) Provider {
	return func(ctx context.Context, exportName string) (string, error) {
		identities, err := adminClusterClient.Cluster(core.RootCluster.Path()).AdminV1alpha1().APIIdentities().List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", fmt.Errorf("admin workspace: %w", err)
		}
		if hash, found := coreIdentityFrom(identities.Items, exportName); found {
			return hash, nil
		}
		return "", fmt.Errorf("admin workspace: no APIIdentity for %s: %w", exportName, ErrUnknown)
	}
}

// coreIdentityFrom returns the identity owned by the named core export in
// the root workspace, if the registry lists one.
func coreIdentityFrom(identities []adminv1alpha1.APIIdentity, exportName string) (string, bool) {
	for i := range identities {
		id := &identities[i]
		if !id.Status.Core || id.Status.Owner == nil {
			continue
		}
		if id.Status.Owner.Export == exportName && id.Status.Owner.Cluster == core.RootCluster.String() {
			return id.Name, true
		}
	}
	return "", false
}

// CacheProvider reads the identity from the root workspace's APIExports as
// replicated to the cache server.
func CacheProvider(cacheKcpClusterClient kcpclientset.ClusterInterface) Provider {
	return func(ctx context.Context, exportName string) (string, error) {
		exports, err := cacheKcpClusterClient.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", fmt.Errorf("cache server: root APIExports: %w", err)
		}
		for i := range exports.Items {
			if exports.Items[i].Name == exportName && exports.Items[i].Status.IdentityHash != "" {
				return exports.Items[i].Status.IdentityHash, nil
			}
		}
		return "", fmt.Errorf("cache server has no root APIExport %s with an identity: %w", exportName, ErrUnknown)
	}
}

// LegacyConfigMapProvider reads the identity from the deprecated
// apiexport-identity-cache ConfigMap in the shard's system:shard logical
// cluster, where the identity cache controller records the identities the
// shard resolved. It lets a shard that has served before restart with
// neither the cache server nor the root shard reachable.
func LegacyConfigMapProvider(kubeClusterClient kcpkubernetesclientset.ClusterInterface) Provider {
	return func(ctx context.Context, exportName string) (string, error) {
		cm, err := kubeClusterClient.Cluster(configshard.SystemShardCluster.Path()).CoreV1().ConfigMaps("default").Get(ctx, identitycache.ConfigMapName, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("local identity cache: %w", err)
		}
		hash, found := cm.Data[exportName]
		if !found || hash == "" {
			return "", fmt.Errorf("local identity cache has no entry for %s: %w", exportName, ErrUnknown)
		}
		return hash, nil
	}
}

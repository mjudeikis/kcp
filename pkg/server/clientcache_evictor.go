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

package server

import (
	"context"

	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
)

// clientCacheEvictor is the subset of every cluster-scoped clientset in
// github.com/kcp-dev/client-go (and kcp-dev/sdk) that exposes per-cluster
// cache eviction. The top-level ClusterClientset.Evict fans out to all of
// its typed sub-clientsets, so the server only needs to track top-level
// clientsets here.
type clientCacheEvictor interface {
	Evict(logicalcluster.Path)
}

// installClientCacheEvictor registers a LogicalCluster delete handler that
// calls Evict on every supplied cluster clientset. Without this, the
// per-cluster client caches grow monotonically and pin per-cluster REST
// clients, codec factories, JSON-decoded schemas, etc. for the lifetime of
// the process. See https://github.com/kcp-dev/kcp/issues/4071.
func installClientCacheEvictor(ctx context.Context, informer corev1alpha1informers.LogicalClusterClusterInformer, evictors []clientCacheEvictor) {
	logger := klog.FromContext(ctx)
	_, _ = informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj any) {
			lc, ok := obj.(*corev1alpha1.LogicalCluster)
			if !ok {
				tombstone, tok := obj.(cache.DeletedFinalStateUnknown)
				if !tok {
					return
				}
				lc, ok = tombstone.Obj.(*corev1alpha1.LogicalCluster)
				if !ok {
					return
				}
			}
			name := logicalcluster.From(lc)
			if name == "" {
				return
			}
			logger.V(4).Info("evicting per-cluster client caches", "logicalcluster", name)
			for _, e := range evictors {
				e.Evict(name.Path())
			}
		},
	})
}

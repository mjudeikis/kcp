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

package apiexportendpointsliceurls

import (
	"context"
	"net/url"
	"path"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/logging"
	apiexportbuilder "github.com/kcp-dev/kcp/pkg/virtual/apiexport/builder"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

type endpointsReconciler struct {
	listShards                 func(selector labels.Selector) ([]*corev1alpha1.Shard, error)
	getAPIExport               func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	listAPIBindingsByAPIExport func(apiexport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error)
	shardName                  string
}

type result struct {
	url    string
	remove bool
}

func (c *controller) reconcile(ctx context.Context, apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) (*result, error) {
	r := &endpointsReconciler{
		listShards:                 c.listShards,
		getAPIExport:               c.getAPIExport,
		listAPIBindingsByAPIExport: c.listAPIBindingsByAPIExport,
		shardName:                  c.shardName,
	}

	return r.reconcile(ctx, apiExportEndpointSlice)
}

func (r *endpointsReconciler) reconcile(ctx context.Context, apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) (*result, error) {
	if !conditions.IsTrue(apiExportEndpointSlice, apisv1alpha1.APIExportEndpointSliceReadyForURLs) {
		return nil, nil
	}

	s := apiExportEndpointSlice.Status.ShardSelector
	if s == "" { // should never happen.
		return nil, nil
	}

	selector, err := labels.Parse(s)
	if err != nil {
		return nil, err
	}

	// Get APIExport
	apiExportPath := logicalcluster.NewPath(apiExportEndpointSlice.Spec.APIExport.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.From(apiExportEndpointSlice).Path()
	}
	apiExport, err := r.getAPIExport(apiExportPath, apiExportEndpointSlice.Spec.APIExport.Name)
	if err != nil {
		return nil, err
	}

	shards, err := r.listShards(selector)
	if err != nil {
		return nil, err
	}

	rs, err := r.updateEndpoints(ctx, apiExportEndpointSlice, apiExport, shards)
	if err != nil {
		return nil, err
	}

	return rs, nil
}

func (r *endpointsReconciler) updateEndpoints(ctx context.Context,
	apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice,
	apiExport *apisv1alpha1.APIExport,
	shards []*corev1alpha1.Shard) (*result, error) {
	logger := klog.FromContext(ctx)
	var rs result
	for _, shard := range shards {
		if shard.Name != r.shardName {
			continue
		}
		if shard.Spec.VirtualWorkspaceURL == "" {
			continue
		}

		// Check if we have local consumers
		bindings, err := r.listAPIBindingsByAPIExport(apiExport)
		if err != nil {
			return nil, err
		}

		if len(bindings) == 0 {
			return &result{
				remove: true,
			}, nil
		}

		u, err := url.Parse(shard.Spec.VirtualWorkspaceURL)
		if err != nil {
			// Should never happen
			logger = logging.WithObject(logger, shard)
			logger.Error(
				err, "error parsing shard.spec.virtualWorkspaceURL",
				"VirtualWorkspaceURL", shard.Spec.VirtualWorkspaceURL,
			)
			continue
		}

		u.Path = path.Join(
			u.Path,
			virtualworkspacesoptions.DefaultRootPathPrefix,
			apiexportbuilder.VirtualWorkspaceName,
			logicalcluster.From(apiExport).String(),
			apiExport.Name,
		)

		rs.url = u.String()
		break
	}

	for _, u := range apiExportEndpointSlice.Status.APIExportEndpoints {
		if u.URL == rs.url {
			return nil, nil
		}
	}

	return &rs, nil
}

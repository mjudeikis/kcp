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

package builder

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"
)

// withAggregateReadView decorates the StoreFuncs so that every read is served
// from the cache server across all shards, presented as one flat collection
// with the cache bookkeeping annotations stripped. Objects of the same name
// in several logical clusters collapse to one, preferring the shard-local
// system:shard copy.
func withAggregateReadView(resource schema.GroupResource) forwardingregistry.StorageWrapper {
	return forwardingregistry.StorageWrapperFunc(func(_ schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		delegateList := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			result, err := delegateList(sourceContext(ctx), options)
			if err != nil {
				return nil, err
			}
			list := result.(*unstructured.UnstructuredList)
			list.Items = dedupeShards(list.Items)
			for i := range list.Items {
				fixupShard(&list.Items[i])
			}
			return list, nil
		}

		storage.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			// see withShardsView: a name-only GET cannot be routed to one
			// logical cluster, so list and pick.
			listOptions := &metainternalversion.ListOptions{}
			if options != nil {
				listOptions.ResourceVersion = options.ResourceVersion
			}
			result, err := storage.ListerFunc(ctx, listOptions)
			if err != nil {
				return nil, err
			}
			list := result.(*unstructured.UnstructuredList)
			for i := range list.Items {
				if list.Items[i].GetName() == name {
					return &list.Items[i], nil
				}
			}
			return nil, apierrors.NewNotFound(resource, name)
		}

		delegateWatch := storage.WatcherFunc
		storage.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
			w, err := delegateWatch(sourceContext(ctx), options)
			if err != nil {
				return nil, err
			}
			result, err := delegateList(sourceContext(ctx), &metainternalversion.ListOptions{})
			if err != nil {
				w.Stop()
				return nil, err
			}
			list, ok := result.(*unstructured.UnstructuredList)
			if !ok {
				w.Stop()
				return nil, fmt.Errorf("expected *unstructured.UnstructuredList when seeding the %s watch, got %T", resource, result)
			}
			return newFixupWatch(ctx, w, list.Items), nil
		}
	})
}

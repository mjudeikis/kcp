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
	"fmt"
	"reflect"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	apisv1alpha1apply "github.com/kcp-dev/kcp/sdk/client/applyconfiguration/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
)

const (
	ControllerName = "kcp-apiexportendpointslice-urls"
)

// NewController returns a new controller for APIExportEndpointSlices.
// Shards and APIExports are read from the cache server.
func NewController(
	shardName string,
	apiExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
	globalAPIExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
	globalShardClusterInformer corev1alpha1informers.ShardClusterInformer,
	globalAPIExportClusterInformer apisv1alpha1informers.APIExportClusterInformer,
	clusterClient kcpclientset.ClusterInterface,
) (*controller, error) {
	c := &controller{
		shardName:     shardName,
		clusterClient: clusterClient,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		listAPIExportEndpointSlices: func() ([]*apisv1alpha1.APIExportEndpointSlice, error) {
			return apiExportEndpointSliceClusterInformer.Lister().List(labels.Everything())
		},
		listShards: func(selector labels.Selector) ([]*corev1alpha1.Shard, error) {
			return globalShardClusterInformer.Lister().List(selector)
		},
		getAPIExportEndpointSlice: func(ctx context.Context, path logicalcluster.Path, name string) (*apisv1alpha1.APIExportEndpointSlice, error) {
			obj, err := indexers.ByPathAndNameWithFallback[*apisv1alpha1.APIExportEndpointSlice](apisv1alpha1.Resource("apiexportendpointslices"), apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), globalAPIExportEndpointSliceClusterInformer.Informer().GetIndexer(), path, name)
			if err != nil {
				return nil, err
			}
			return obj, err
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			return indexers.ByPathAndName[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), globalAPIExportClusterInformer.Informer().GetIndexer(), path, name)
		},
		listAPIBindingsByAPIExport: func(export *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error) {
			// binding keys by full path
			keys := sets.New[string]()
			if path := logicalcluster.NewPath(export.Annotations[core.LogicalClusterPathAnnotationKey]); !path.Empty() {
				pathKeys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, path.Join(export.Name).String())
				if err != nil {
					return nil, err
				}
				keys.Insert(pathKeys...)
			}

			clusterKeys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, logicalcluster.From(export).Path().Join(export.Name).String())
			if err != nil {
				return nil, err
			}
			keys.Insert(clusterKeys...)

			bindings := make([]*apisv1alpha1.APIBinding, 0, keys.Len())
			for _, key := range sets.List[string](keys) {
				binding, exists, err := apiBindingInformer.Informer().GetIndexer().GetByKey(key)
				if err != nil {
					utilruntime.HandleError(err)
					continue
				} else if !exists {
					utilruntime.HandleError(fmt.Errorf("APIBinding %q does not exist", key))
					continue
				}
				bindings = append(bindings, binding.(*apisv1alpha1.APIBinding))
			}
			return bindings, nil
		},
		apiExportEndpointSliceClusterInformer:       apiExportEndpointSliceClusterInformer,
		globalApiExportEndpointSliceClusterInformer: globalAPIExportEndpointSliceClusterInformer,
	}

	_, _ = apiExportEndpointSliceClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlice(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIExportEndpointSlice(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlice(obj)
		},
	})

	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSliceByAPIBinding(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIExportEndpointSliceByAPIBinding(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSliceByAPIBinding(obj)
		},
	})

	_, _ = globalAPIExportEndpointSliceClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSliceFromCache(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIExportEndpointSliceFromCache(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSliceFromCache(obj)
		},
	}))

	_, _ = globalAPIExportClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlicesForAPIExport(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlicesForAPIExport(obj)
		},
	}))

	_, _ = globalShardClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAllAPIExportEndpointSlices(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if filterShardEvent(oldObj, newObj) {
				c.enqueueAllAPIExportEndpointSlices(newObj)
			}
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAllAPIExportEndpointSlices(obj)
		},
	}))

	return c, nil
}

// controller reconciles APIExportEndpointSlices. It ensures that the shard endpoints are populated
// in the status of every APIExportEndpointSlices.
type controller struct {
	queue         workqueue.TypedRateLimitingInterface[string]
	shardName     string
	clusterClient kcpclientset.ClusterInterface

	listShards                  func(selector labels.Selector) ([]*corev1alpha1.Shard, error)
	listAPIExportEndpointSlices func() ([]*apisv1alpha1.APIExportEndpointSlice, error)
	getAPIExportEndpointSlice   func(ctx context.Context, path logicalcluster.Path, name string) (*apisv1alpha1.APIExportEndpointSlice, error)
	getAPIExport                func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	listAPIBindingsByAPIExport  func(apiexport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error)

	apiExportEndpointSliceClusterInformer       apisv1alpha1informers.APIExportEndpointSliceClusterInformer
	globalApiExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer
}

func (c *controller) enqueueAPIExportEndpointSliceByAPIBinding(obj interface{}) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	binding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("obj is supposed to be a APIBinding, but is %T", obj))
		return
	}

	{ // local to shard
		keys := sets.New[string]()
		if path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path); !path.Empty() {
			pathKeys, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexAPIExportEndpointSliceByAPIExport, path.Join(binding.Spec.Reference.Export.Name).String())
			if err != nil {
				utilruntime.HandleError(err)
				return
			}
			keys.Insert(pathKeys...)
		}

		for _, key := range sets.List[string](keys) {
			slice, exists, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().GetByKey(key)
			if err != nil {
				utilruntime.HandleError(err)
				continue
			} else if !exists {
				continue
			}
			logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*apisv1alpha1.APIBinding))
			logging.WithQueueKey(logger, key).V(4).Info("queuing APIExportEndpointSlices because of consumer APIBinding")
			c.enqueueAPIExportEndpointSlice(slice)
		}
	}
	{
		keys := sets.New[string]()
		if path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path); !path.Empty() {
			pathKeys, err := c.globalApiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexAPIExportEndpointSliceByAPIExport, path.Join(binding.Spec.Reference.Export.Name).String())
			if err != nil {
				utilruntime.HandleError(err)
				return
			}
			keys.Insert(pathKeys...)
		}

		for _, key := range sets.List[string](keys) {
			slice, exists, err := c.globalApiExportEndpointSliceClusterInformer.Informer().GetIndexer().GetByKey(key)
			if err != nil {
				utilruntime.HandleError(err)
				continue
			} else if !exists {
				continue
			}
			logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*apisv1alpha1.APIBinding))
			logging.WithQueueKey(logger, key).V(4).Info("queuing APIExportEndpointSlices because of consumer APIBinding from cache")
			c.enqueueAPIExportEndpointSlice(slice)
		}
	}
}

// enqueueAPIExportEndpointSlice enqueues an APIExportEndpointSlice.
func (c *controller) enqueueAPIExportEndpointSlice(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing APIExportEndpointSlice")
	c.queue.Add(key)
}

// enqueueAPIExportEndpointSlice enqueues an APIExportEndpointSlice.
func (c *controller) enqueueAPIExportEndpointSliceFromCache(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing APIExportEndpointSlice from cache")
	c.queue.Add(key)
}

// enqueueAPIExportEndpointSlicesForAPIExport enqueues APIExportEndpointSlices referencing a specific APIExport.
func (c *controller) enqueueAPIExportEndpointSlicesForAPIExport(obj interface{}) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	export, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("obj is supposed to be a APIExport, but is %T", obj))
		return
	}

	// binding keys by full path
	keys := sets.New[string]()
	if path := logicalcluster.NewPath(export.Annotations[core.LogicalClusterPathAnnotationKey]); !path.Empty() {
		pathKeys, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexAPIExportEndpointSliceByAPIExport, path.Join(export.Name).String())
		if err != nil {
			utilruntime.HandleError(err)
			return
		}
		keys.Insert(pathKeys...)
	}

	clusterKeys, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexAPIExportEndpointSliceByAPIExport, logicalcluster.From(export).Path().Join(export.Name).String())
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	keys.Insert(clusterKeys...)

	for _, key := range sets.List[string](keys) {
		slice, exists, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().GetByKey(key)
		if err != nil {
			utilruntime.HandleError(err)
			continue
		} else if !exists {
			continue
		}
		logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*apisv1alpha1.APIExport))
		logging.WithQueueKey(logger, key).V(4).Info("queuing APIExportEndpointSlices because of referenced APIExport")
		c.enqueueAPIExportEndpointSlice(slice)
	}
}

// enqueueAllAPIExportEndpointSlices enqueues all APIExportEndpointSlices.
func (c *controller) enqueueAllAPIExportEndpointSlices(shard interface{}) {
	list, err := c.listAPIExportEndpointSlices()
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), shard.(*corev1alpha1.Shard))
	for i := range list {
		key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(list[i])
		if err != nil {
			utilruntime.HandleError(err)
			continue
		}

		logging.WithQueueKey(logger, key).V(4).Info("queuing APIExportEndpointSlice because Shard changed")
		c.queue.Add(key)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}
	obj, err := c.getAPIExportEndpointSlice(ctx, clusterName.Path(), name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	result, err := c.reconcile(ctx, obj)
	if err != nil {
		errs = append(errs, err)
	}
	if result == nil {
		return utilerrors.NewAggregate(errs)
	}

	patch := apisv1alpha1apply.APIExportEndpointSlice(obj.Name)
	if result.remove {
		patch.WithStatus(apisv1alpha1apply.APIExportEndpointSliceStatus())
	} else {
		patch.WithStatus(apisv1alpha1apply.APIExportEndpointSliceStatus().
			WithAPIExportEndpoints(apisv1alpha1apply.APIExportEndpoint().WithURL(result.url)))
	}

	_, err = c.clusterClient.ApisV1alpha1().APIExportEndpointSlices().Cluster(clusterName.Path()).ApplyStatus(
		ctx,
		patch,
		metav1.ApplyOptions{
			FieldManager: c.shardName,
		},
	)
	if err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

// filterShardEvent returns true if the event passes the filter and needs to be processed false otherwise.
func filterShardEvent(oldObj, newObj interface{}) bool {
	oldShard, ok := oldObj.(*corev1alpha1.Shard)
	if !ok {
		return false
	}
	newShard, ok := newObj.(*corev1alpha1.Shard)
	if !ok {
		return false
	}
	if oldShard.Spec.VirtualWorkspaceURL != newShard.Spec.VirtualWorkspaceURL {
		return true
	}
	if !reflect.DeepEqual(oldShard.Labels, newShard.Labels) {
		return true
	}
	return false
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(
	globalAPIExportClusterInformer apisv1alpha1informers.APIExportClusterInformer,
	globalAPIExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
	apiExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
) {
	indexers.AddIfNotPresentOrDie(globalAPIExportClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(globalAPIExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexAPIExportEndpointSliceByAPIExport: indexAPIExportEndpointSliceByAPIExportFunc,
	})
	indexers.AddIfNotPresentOrDie(globalAPIExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexAPIExportEndpointSliceByAPIExport: indexAPIExportEndpointSliceByAPIExportFunc,
	})
	indexers.AddIfNotPresentOrDie(apiBindingInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIBindingsByAPIExport: indexers.IndexAPIBindingByAPIExport,
	})
}

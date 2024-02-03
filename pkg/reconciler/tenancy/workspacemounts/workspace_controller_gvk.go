/*
Copyright 2021 The KCP Authors.

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

package workspacemounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	tenancy "github.com/kcp-dev/kcp/sdk/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

// enqueueForResource adds the resource (gvr + obj) to the queue used for mounts.
func (c *Controller) enqueueForResource(logger logr.Logger, gvr schema.GroupVersionResource, obj interface{}) {
	queueKey := strings.Join([]string{gvr.Resource, gvr.Version, gvr.Group}, ".")

	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	queueKey += "::" + key
	logging.WithQueueKey(logger, queueKey).V(2).Info("queuing gvr resource")
	c.gvrQueue.Add(queueKey)
}

func (c *Controller) startGVKWorker(ctx context.Context) {
	for c.processNextGVKWorkItem(ctx) {
	}
}

func (c *Controller) processNextGVKWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.gvrQueue.Get()
	if quit {
		return false
	}
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.gvrQueue.Done(key)

	if err := c.processGVK(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.gvrQueue.AddRateLimited(key)
		return true
	}
	c.gvrQueue.Forget(key)
	return true
}

func (c *Controller) processGVK(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	parts := strings.SplitN(key, "::", 2)
	if len(parts) != 2 {
		logger.Error(errors.New("unexpected key format"), "skipping key")
		return nil
	}

	gvr, _ := schema.ParseResourceArg(parts[0])
	if gvr == nil {
		logger.Error(errors.New("unable to parse gvr string"), "skipping key", "gvr", parts[0])
		return nil
	}
	key = parts[1]

	logger = logger.WithValues("gvr", gvr.String(), "name", key)

	inf, err := c.discoveringDynamicSharedInformerFactory.ForResource(*gvr)
	if err != nil {
		return fmt.Errorf("error getting dynamic informer for GVR %q: %w", gvr, err)
	}

	obj, exists, err := inf.Informer().GetIndexer().GetByKey(key)
	if err != nil {
		logger.Error(err, "unable to get from indexer")
		return nil // retrying won't help
	}
	if !exists {
		logger.V(4).Info("resource not found")
		return nil
	}

	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		logger.Error(nil, "got unexpected type", "type", fmt.Sprintf("%T", obj))
		return nil // retrying won't help
	}
	u = u.DeepCopy()

	logger = logging.WithObject(logger, u)
	ctx = klog.NewContext(ctx, logger)

	if u.GetAnnotations() == nil {
		return nil
	}

	// We only care about mount objects
	val, ok := u.GetAnnotations()[tenancyv1alpha1.ExperimentalIsMountAnnotationKey]
	if !ok || val != "true" {
		return nil
	}

	ownerRaw, ok := u.GetAnnotations()[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]
	if !ok {
		return nil
	}

	var ownerSchema metav1.OwnerReference
	if err := json.Unmarshal([]byte(ownerRaw), &ownerSchema); err != nil {
		return fmt.Errorf("unable to unmarshal owner reference: %w", err)
	}

	if ownerSchema.Kind != tenancy.WorkspaceKind {
		return fmt.Errorf("owner reference is not a workspace: %s", ownerSchema.Kind)
	}

	// queue workspace
	cluster := logicalcluster.From(u)
	keyWorkspace := kcpcache.ToClusterAwareKey(cluster.String(), "", ownerSchema.Name)

	c.queue.Add(keyWorkspace)

	return nil
}

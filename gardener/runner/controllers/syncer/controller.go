package syncer

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/kcp/gardener/runner/mutators"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	mcsource "sigs.k8s.io/multicluster-runtime/pkg/source"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	controllerName = "syncer-controller"
)

// Reconciler reconciles a GVR object to the provider cluster.
type Reconciler struct {
	// providerClient is a normal k8s client for the provider cluster.
	providerClient ctrlruntimeclient.Client
	// consumerManager manages the source clusters, which is multicluster runtime enabled.
	consumerManager mcmanager.Manager

	gvk       schema.GroupVersionKind
	agentName string
}

// Create creates a new controller with watches for source and provider clusters.
func Create(
	ctx context.Context,
	providerManager manager.Manager,
	consumerManager mcmanager.Manager,
	gvk schema.GroupVersionKind,
	agentName string,
	log klog.Logger,
	numWorkers int,
) (mccontroller.Controller, error) {
	// Create dummy objects for watching
	consumerDummy := &unstructured.Unstructured{}
	consumerDummy.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind))

	providerDummy := &unstructured.Unstructured{}
	providerDummy.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind))

	// Setup the reconciler
	reconciler := &Reconciler{
		providerClient:  providerManager.GetClient(),
		consumerManager: consumerManager,
		gvk:             gvk,
		agentName:       agentName,
	}

	ctrlOptions := mccontroller.Options{
		Reconciler:              reconciler,
		MaxConcurrentReconciles: numWorkers,
		SkipNameValidation:      ptr.To(true),
		Logger:                  log,
	}

	log.Info("Setting up unmanaged controller...")

	// Create unmanaged multicluster controller for consumer clusters
	c, err := mccontroller.NewUnmanaged(controllerName, consumerManager, ctrlOptions)
	if err != nil {
		return nil, err
	}

	// Watch the target resource in the provider clusters
	if err := c.MultiClusterWatch(mcsource.TypedKind(consumerDummy, mchandler.TypedEnqueueRequestForObject[*unstructured.Unstructured]())); err != nil {
		return nil, err
	}

	// enqueueConsumerObjForProviderObj maps provider objects back to consumer objects by determinings cluster mapping
	enqueueConsumerObjForProviderObj := handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o *unstructured.Unstructured) []mcreconcile.Request {
		// Determine target consumer cluster - could be based on labels, annotations, or configuration
		targetCluster := getTargetProviderCluster(o)
		if targetCluster == "" {
			log.V(4).Info("No target cluster found for object. Skipping", "namespace", o.GetNamespace(), "name", o.GetName())
			// No specific target cluster, skip reconciliation
			return nil
		}

		return []mcreconcile.Request{
			{
				ClusterName: targetCluster,
				Request: reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: o.GetNamespace(),
						Name:      o.GetName(),
					},
				},
			},
		}
	})

	// Only watch source objects that we manage
	nameFilter := predicate.NewTypedPredicateFuncs(func(u *unstructured.Unstructured) bool {
		return true // Add filtering logic if needed
	})

	if err := c.Watch(source.TypedKind(providerManager.GetCache(), providerDummy, enqueueConsumerObjForProviderObj, nameFilter)); err != nil {
		return nil, err
	}

	log.Info("Done setting up unmanaged controller.")

	return c, nil
}

// Reconcile syncs the spec to provider and status back from provider.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("cluster", req.ClusterName, "namespace", req.NamespacedName.Namespace, "name", req.NamespacedName.Name)
	logger.Info("Processing")

	// Get provider cluster client
	cl, err := r.consumerManager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get cluster: %w", err)
	}

	consumerClient := cl.GetClient()
	providerClient := r.providerClient

	// Get the source object
	consumerObj := &unstructured.Unstructured{}
	consumerObj.SetGroupVersionKind(r.gvk.GroupVersion().WithKind(r.gvk.Kind))
	if err := consumerClient.Get(ctx, req.NamespacedName, consumerObj); err != nil {
		if ctrlruntimeclient.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get source object: %w", err)
		}
		// Object not found, delete from provider if it exists
		return r.deleteFromProvider(ctx, providerClient, req.NamespacedName)
	}

	if consumerObj.GetDeletionTimestamp() != nil {
		// Object is being deleted, delete from provider
		r, err := r.deleteFromProvider(ctx, providerClient, req.NamespacedName)
		if err != nil || r.RequeueAfter > 0 { // once deleted from provider, remove finalizer, before that - wait
			return r, err
		}
		logger.Info("Object is being deleted, deleted from provider if existed")
		consumerObj.SetFinalizers(nil)
		if err := consumerClient.Update(ctx, consumerObj); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from consumer object: %w", err)
		}

		return ctrl.Result{}, nil
	}

	providerObj := &unstructured.Unstructured{}
	providerObj.SetGroupVersionKind(r.gvk.GroupVersion().WithKind(r.gvk.Kind))
	err = providerClient.Get(ctx, req.NamespacedName, providerObj)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Provider object does not exist, create it
			logger.Info("Provider object not found, creating")
			if err := r.syncToProvider(ctx, providerClient, consumerObj, nil); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to sync spec to provider: %w", err)
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil // Requeue to check status later
		}
		return ctrl.Result{}, fmt.Errorf("failed to get provider object: %w", err)
	}

	// Sync spec to provider
	logger.Info("Sync spec to provider")
	if err := r.syncToProvider(ctx, providerClient, consumerObj, providerObj); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update spec to provider: %w", err)
	}

	logger.Info("Sync status from provider")
	// Sync status back from provider
	if err := r.syncFromProvider(ctx, consumerClient, consumerObj, providerObj); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to sync status from provider: %w", err)
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// syncSpecToProvider syncs the object spec to the provider cluster.
func (r *Reconciler) syncToProvider(ctx context.Context, providerClient ctrlruntimeclient.Client, consumerObj, providerObj *unstructured.Unstructured) error {
	// Mutate the object before syncing
	mutator, err := mutators.GetMutator(r.gvk)
	if err != nil {
		return fmt.Errorf("failed to get mutator for GVK %s: %w", r.gvk.String(), err)
	}

	isCreate := providerObj == nil
	var currentProviderObj *unstructured.Unstructured
	if !isCreate {
		currentProviderObj = providerObj.DeepCopy()
	} else {
		// Initialize a new provider object for creation
		providerObj = &unstructured.Unstructured{}
		providerObj.SetGroupVersionKind(consumerObj.GetObjectKind().GroupVersionKind())
		providerObj.SetNamespace(consumerObj.GetNamespace())
		providerObj.SetName(consumerObj.GetName())
	}

	err = mutator.ToProvider(consumerObj, providerObj)
	if err != nil {
		return fmt.Errorf("failed to mutate object to provider format: %w", err)
	}

	if isCreate {
		if err := providerClient.Create(ctx, providerObj); err != nil {
			return fmt.Errorf("failed to create object in provider: %w", err)
		}
		return nil
	} else {

		// Check diff to avoid unnecessary updates
		logger := klog.FromContext(ctx)
		logger.V(4).Info("Comparing provider object for spec update", "diff", cmp.Diff(currentProviderObj.Object["spec"], providerObj.Object["spec"]))
		if cmp.Diff(currentProviderObj.Object["spec"], providerObj.Object["spec"]) == "" {
			logger.V(4).Info("No spec changes detected, skipping update to provider")
			return nil
		}

		if err := providerClient.Update(ctx, providerObj); err != nil {
			return fmt.Errorf("failed to update object in provider: %w", err)
		}
	}

	return nil
}

// syncStatusFromProvider syncs the object status back from the provider cluster.
func (r *Reconciler) syncFromProvider(ctx context.Context, consumerClient ctrlruntimeclient.Client, consumerObj, providerObj *unstructured.Unstructured) error {
	//logger := klog.FromContext(ctx)
	// Mutate the object before syncing
	mutator, err := mutators.GetMutator(r.gvk)
	if err != nil {
		return fmt.Errorf("failed to get mutator for GVK %s: %w", r.gvk.String(), err)
	}

	currentConsumerObj := consumerObj.DeepCopy()

	err = mutator.ToConsumer(providerObj, consumerObj)
	if err != nil {
		return fmt.Errorf("failed to mutate object to consumer format: %w", err)
	}

	// Check diff to avoid unnecessary updates
	logger := klog.FromContext(ctx)
	logger.V(4).Info("Comparing consumer object for status update", "diff", cmp.Diff(currentConsumerObj.Object["status"], consumerObj.Object["status"]))
	if cmp.Diff(currentConsumerObj.Object["status"], consumerObj.Object["status"]) == "" {
		logger.V(4).Info("No status changes detected, skipping update to consumer")
		return nil
	}

	err = consumerClient.Update(ctx, consumerObj.DeepCopy())
	if err != nil {
		return fmt.Errorf("failed to update object in consumer: %w", err)
	}

	if err := consumerClient.Status().Update(ctx, consumerObj); err != nil {
		return fmt.Errorf("failed to update object in consumer: %w", err)
	}

	return nil
}

// deleteFromProvider deletes the object from the provider cluster.
func (r *Reconciler) deleteFromProvider(ctx context.Context, providerClient ctrlruntimeclient.Client, namespacedName types.NamespacedName) (ctrl.Result, error) {
	providerObj := &unstructured.Unstructured{}
	providerObj.SetGroupVersionKind(r.gvk.GroupVersion().WithKind(r.gvk.Kind))
	err := providerClient.Get(ctx, namespacedName, providerObj)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Already deleted
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get provider object for deletion: %w", err)
	}
	// Delete is 2 step process. Add annotation: confirmation.gardener.cloud/deletion=true to config, then delete.
	// Witout the annotation, Gardener will not delete the object, even if deleteTimestamp is set.
	if ann, ok := providerObj.GetAnnotations()["confirmation.gardener.cloud/deletion"]; !ok || ann != "true" {
		if providerObj.GetAnnotations() == nil {
			providerObj.SetAnnotations(map[string]string{})
		}
		ann := providerObj.GetAnnotations()
		ann["confirmation.gardener.cloud/deletion"] = "true"
		providerObj.SetAnnotations(ann)
		if err := providerClient.Update(ctx, providerObj); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add deletion confirmation annotation: %w", err)
		}
		logger := klog.FromContext(ctx).WithValues("namespace", namespacedName.Namespace, "name", namespacedName.Name)
		logger.Info("Added deletion confirmation annotation to provider object")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if err := providerClient.Delete(ctx, providerObj); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to delete provider object: %w", err)
	}

	logger := klog.FromContext(ctx).WithValues("namespace", namespacedName.Namespace, "name", namespacedName.Name)
	logger.Info("Deleted object from provider cluster")

	return ctrl.Result{RequeueAfter: time.Second * 10}, nil
}

// getTargetProviderCluster determines which provider cluster to sync the object to.
func getTargetProviderCluster(obj *unstructured.Unstructured) string {
	// Check for cluster preference in labels
	ann := obj.GetAnnotations()
	if ann != nil {
		if cluster, exists := ann[mutators.ConsumerClusterAnnotation]; exists {
			return cluster
		}
	}
	return "" // Empty - not our concern
}

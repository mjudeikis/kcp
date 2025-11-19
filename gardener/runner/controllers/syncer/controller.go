package syncer

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

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

	// OriginClusterAnnotation stores the cluster name where the object originated
	OriginClusterAnnotation = "syncer.kcp.io/origin-cluster"
)

// Reconciler reconciles a GVR object to the provider cluster.
type Reconciler struct {
	// providerClient is a normal k8s client for the provider cluster.
	providerClient ctrlruntimeclient.Client
	// consumerManager manages the source clusters, which is multicluster runtime enabled.
	consumerManager mcmanager.Manager

	gvk       schema.GroupVersionKind
	log       klog.Logger
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
		log:             log,
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
		return isOwnedBy(u, agentName)
	})

	if err := c.Watch(source.TypedKind(providerManager.GetCache(), providerDummy, enqueueConsumerObjForProviderObj, nameFilter)); err != nil {
		return nil, err
	}

	log.Info("Done setting up unmanaged controller.")

	return c, nil
}

// Reconcile syncs the spec to provider and status back from provider.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := r.log.WithValues("cluster", req.ClusterName, "namespace", req.NamespacedName.Namespace, "name", req.NamespacedName.Name)
	logger.Info("Processing")

	// Get provider cluster client
	cl, err := r.consumerManager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get cluster: %w", err)
	}
	providerClient := cl.GetClient()

	// Get the source object
	sourceObj := &unstructured.Unstructured{}
	sourceObj.SetGroupVersionKind(r.gvk.GroupVersion().WithKind(r.gvk.Kind))
	if err := providerClient.Get(ctx, req.NamespacedName, sourceObj); err != nil {
		if ctrlruntimeclient.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get source object: %w", err)
		}
		// Object not found, delete from provider if it exists
		return r.deleteFromProvider(ctx, providerClient, req.NamespacedName)
	}

	// Sync spec to provider
	if err := r.syncSpecToProvider(ctx, providerClient, sourceObj); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to sync spec to provider: %w", err)
	}

	// Sync status back from provider
	if err := r.syncStatusFromProvider(ctx, providerClient, sourceObj); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to sync status from provider: %w", err)
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// syncSpecToProvider syncs the object spec to the provider cluster.
func (r *Reconciler) syncSpecToProvider(ctx context.Context, providerClient ctrlruntimeclient.Client, sourceObj *unstructured.Unstructured) error {
	providerObj := &unstructured.Unstructured{}
	providerObj.SetGroupVersionKind(sourceObj.GetObjectKind().GroupVersionKind())
	providerObj.SetNamespace(sourceObj.GetNamespace())
	providerObj.SetName(sourceObj.GetName())

	// Add origin cluster annotation to preserve source cluster information
	annotations := providerObj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	cluster := logicalcluster.From(sourceObj)

	annotations[OriginClusterAnnotation] = cluster.String()
	providerObj.SetAnnotations(annotations)

	// Copy spec from source to provider object
	if spec, found, err := unstructured.NestedMap(sourceObj.Object, "spec"); err != nil {
		return fmt.Errorf("failed to get spec from source object: %w", err)
	} else if found {
		if err := unstructured.SetNestedMap(providerObj.Object, spec, "spec"); err != nil {
			return fmt.Errorf("failed to set spec in provider object: %w", err)
		}
	}

	// Create or update in provider cluster
	if err := providerClient.Create(ctx, providerObj); err != nil {
		if ctrlruntimeclient.IgnoreAlreadyExists(err) != nil {
			return fmt.Errorf("failed to create object in provider: %w", err)
		}
		// Object exists, update it
		existingObj := &unstructured.Unstructured{}
		existingObj.SetGroupVersionKind(providerObj.GetObjectKind().GroupVersionKind())
		if err := providerClient.Get(ctx, types.NamespacedName{
			Namespace: providerObj.GetNamespace(),
			Name:      providerObj.GetName(),
		}, existingObj); err != nil {
			return fmt.Errorf("failed to get existing object from provider: %w", err)
		}

		// Update annotations to preserve origin cluster
		annotations := existingObj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[OriginClusterAnnotation] = cluster.String()
		existingObj.SetAnnotations(annotations)

		// Update spec
		if spec, found, err := unstructured.NestedMap(sourceObj.Object, "spec"); err != nil {
			return fmt.Errorf("failed to get spec from source object: %w", err)
		} else if found {
			if err := unstructured.SetNestedMap(existingObj.Object, spec, "spec"); err != nil {
				return fmt.Errorf("failed to set spec in existing object: %w", err)
			}
		}

		if err := providerClient.Update(ctx, existingObj); err != nil {
			return fmt.Errorf("failed to update object in provider: %w", err)
		}
	}

	return nil
}

// syncStatusFromProvider syncs the object status back from the provider cluster.
func (r *Reconciler) syncStatusFromProvider(ctx context.Context, providerClient ctrlruntimeclient.Client, sourceObj *unstructured.Unstructured) error {
	// TODO: Implement status sync logic as needed.

	return nil
}

// deleteFromProvider deletes the object from the provider cluster.
func (r *Reconciler) deleteFromProvider(ctx context.Context, providerClient ctrlruntimeclient.Client, namespacedName types.NamespacedName) (ctrl.Result, error) {
	// TODO: Implement deletion logic as needed.
	return ctrl.Result{}, nil
}

// getTargetProviderCluster determines which provider cluster to sync the object to.
func getTargetProviderCluster(obj *unstructured.Unstructured) string {
	// Check for cluster preference in labels
	ann := obj.GetAnnotations()
	if ann != nil {
		if cluster, exists := ann["syncer.kcp.io/target-cluster"]; exists {
			return cluster
		}
	}
	return "this-does-not-exist" // Default cluster name if none specified
}

// isOwnedBy checks if an object is owned by the specified agent.
func isOwnedBy(obj *unstructured.Unstructured, agentName string) bool {
	ann := obj.GetAnnotations()
	if ann == nil {
		return false
	}
	owner, exists := ann["syncer.kcp.io/owner"]
	return exists && owner == agentName
}

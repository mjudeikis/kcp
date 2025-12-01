package related

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/kcp/gardener/runner/mutators"
	"github.com/kcp-dev/logicalcluster/v3"
	"k8s.io/client-go/dynamic/dynamiclister"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	predicateregistry "github.com/kcp-dev/kcp/gardener/runner/predicates"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"k8s.io/client-go/dynamic/dynamicinformer"
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
	controllerName = "related-syncer-controller"
)

// Reconciler reconciles a GVR object to the provider cluster.
type Reconciler struct {
	// providerClient is a normal k8s client for the provider cluster.
	providerClient ctrlruntimeclient.Client
	// consumerManager manages the source clusters, which is multicluster runtime enabled.
	consumerManager mcmanager.Manager

	gvk schema.GroupVersionKind
}

// Create creates a new controller with watches for source and provider clusters.
func Create(
	ctx context.Context,
	providerManager manager.Manager,
	consumerManager mcmanager.Manager,
	dynamicProviderInformer dynamicinformer.DynamicSharedInformerFactory,
	ownerGVK schema.GroupVersionKind,
	gvk schema.GroupVersionKind,
	predicateRegistry predicateregistry.Registry,
	log klog.Logger,
	numWorkers int,
) (mccontroller.Controller, error) {
	// Create dummy objects for watching
	consumerDummy := &unstructured.Unstructured{}
	consumerDummy.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind))

	providerDummy := &unstructured.Unstructured{}
	providerDummy.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind))

	providerOwnerDummy := &unstructured.Unstructured{}
	providerOwnerDummy.SetGroupVersionKind(ownerGVK.GroupVersion().WithKind(ownerGVK.Kind))

	// Setup the reconciler
	reconciler := &Reconciler{
		providerClient:  providerManager.GetClient(),
		consumerManager: consumerManager,
		gvk:             gvk,
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
		return []mcreconcile.Request{
			{
				ClusterName: "unknown", // IMPORTANT: Map targetCluster in the reconciler
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
	filter := predicate.NewTypedPredicateFuncs(func(u *unstructured.Unstructured) bool {
		predicates := predicateRegistry.GetPredicates()
		for name, fn := range predicates {
			log.Info("Applying predicate", "name", name)
			if fn(u) {
				log.Info("Predicate matched, allowing reconciliation", "name", name)
				return true
			}
		}
		return false // Add filtering logic if needed
	})

	gvr := gvk.GroupVersion().WithResource(strings.ToLower(gvk.Kind) + "s") // Poor mans rest mapper - assumes plural is Kind + "s"
	dynamicProviderLister := dynamiclister.New(dynamicProviderInformer.ForResource(gvr).Informer().GetIndexer(), gvr)

	// enqueueOwnerConsumerObjForProviderObj maps provider objects back to consumer objects by determinings cluster mapping
	enqueueOwnerConsumerObjForProviderObj := handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o *unstructured.Unstructured) []mcreconcile.Request {
		// TODO: This could be scoped to label if we had one.
		l := labels.Everything()
		objects, err := dynamicProviderLister.Namespace(o.GetNamespace()).List(l)
		if err != nil {
			log.Error(err, "Failed to list consumer objects for owner reference mapping")
			return []mcreconcile.Request{}
		}

		owners := []metav1.OwnerReference{
			{
				APIVersion: ownerGVK.GroupVersion().String(),
				Kind:       ownerGVK.Kind,
				Name:       o.GetName(),
				UID:        o.GetUID(),
			},
		}

		cluster := logicalcluster.From(o)

		var requests []mcreconcile.Request
		for _, object := range objects {
			// Check if we own this object
			owns, err := controllerutil.HasOwnerReference(owners, object, providerManager.GetScheme())
			if err != nil {
				continue
			}
			if !owns {
				continue
			}

			log.Info("Found owned consumer object for provider owner", "consumerObject", object.GetName(), "ownerObject", o.GetName())
			requests = append(requests, mcreconcile.Request{
				ClusterName: cluster.String(),
				Request: reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: object.GetNamespace(),
						Name:      object.GetName(),
					},
				},
			})
		}

		return requests
	})

	// Only watch source objects that we manage
	filterNone := predicate.NewTypedPredicateFuncs(func(u *unstructured.Unstructured) bool {
		return true
	})

	if err := c.Watch(source.TypedKind(providerManager.GetCache(), providerDummy, enqueueConsumerObjForProviderObj, filter)); err != nil {
		return nil, err
	}
	c.Watch(source.TypedKind(providerManager.GetCache(), providerDummy, enqueueConsumerObjForProviderObj, filter))
	c.Watch(source.TypedKind(providerManager.GetCache(), providerOwnerDummy, enqueueOwnerConsumerObjForProviderObj, filterNone))

	log.Info("Done setting up unmanaged controller.")

	return c, nil
}

// Reconcile syncs the spec to provider and status back from provider.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("cluster", req.ClusterName, "namespace", req.NamespacedName.Namespace, "name", req.NamespacedName.Name)
	logger.Info("Processing")

	// We need to determine the target provider cluster based on the consumer object
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.gvk)

	err := r.providerClient.Get(ctx, types.NamespacedName{
		Namespace: req.NamespacedName.Namespace,
		Name:      req.NamespacedName.Name,
	}, obj)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get owner object from provider cluster: %w", err)
	}

	// All this part is to map back to the correct provider cluster based on the owner object.
	currentShoot := &unstructured.Unstructured{}
	{
		owners := obj.GetOwnerReferences()
		for _, owner := range owners {
			logger.Info("Owner reference found", "owner", owner.Name, "kind", owner.Kind)
			if owner.Kind == "Shoot" {
				parts := strings.Split(owner.APIVersion, "/")
				currentShoot.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   parts[0],
					Version: parts[1],
					Kind:    owner.Kind,
				})
				err := r.providerClient.Get(ctx, types.NamespacedName{
					Name:      owner.Name,
					Namespace: req.Namespace,
				}, currentShoot)
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to get shoot owner object from provider cluster: %w", err)
				}

				targetCluster := getTargetProviderCluster(currentShoot)
				if targetCluster == "" {
					logger.Info("No target provider cluster specified in shoot, skipping reconciliation")
					return ctrl.Result{}, nil
				}

				logger.Info("Determined target provider cluster", "cluster", targetCluster)
				req.ClusterName = targetCluster
				break
			}
		}

		if req.ClusterName == "unknown" {
			logger.Info("Could not determine target provider cluster, skipping reconciliation")
			return ctrl.Result{}, nil
		}
	}
	{
		// Sync the secret to the provider cluster and set onwer reference to the consumer object
		cl, err := r.consumerManager.GetCluster(ctx, req.ClusterName)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get consumer cluster %s: %w", req.ClusterName, err)
		}

		consumerObj := &unstructured.Unstructured{}
		consumerObj.SetGroupVersionKind(r.gvk)

		err = r.providerClient.Get(ctx, types.NamespacedName{
			Namespace: req.NamespacedName.Namespace,
			Name:      req.NamespacedName.Name,
		}, consumerObj)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get consumer object from provider cluster: %w", err)
		}

		consumerShoot := currentShoot.DeepCopy() // reuse the shoot we already fetched
		err = cl.GetClient().Get(ctx, types.NamespacedName{
			Namespace: currentShoot.GetNamespace(),
			Name:      currentShoot.GetName(),
		}, consumerShoot)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get shoot owner object from consumer cluster %s: %w", req.ClusterName, err)
		}

		// TODO: handle updated objects. Deletion should be handled by GC via owner references.
		consumerObj.SetOwnerReferences(nil)
		consumerObj.SetResourceVersion("")
		consumerObj.SetOwnerReferences([]metav1.OwnerReference{
			*metav1.NewControllerRef(consumerShoot, consumerShoot.GetObjectKind().GroupVersionKind()),
		})

		logger.Info("Creating/Updating consumer object in consumer cluster", "object", consumerObj.GetName())

		// Try to create first
		err = cl.GetClient().Create(ctx, consumerObj)
		if err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Object exists, get current version and update
				existingObj := &unstructured.Unstructured{}
				existingObj.SetAPIVersion(consumerObj.GetAPIVersion())
				existingObj.SetKind(consumerObj.GetKind())

				if err := cl.GetClient().Get(ctx, client.ObjectKeyFromObject(consumerObj), existingObj); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to get existing consumer object in consumer cluster %s: %w", req.ClusterName, err)
				}

				// Check if update is needed by comparing object content
				// Remove metadata fields that shouldn't be compared
				existingCopy := existingObj.DeepCopy()
				newCopy := consumerObj.DeepCopy()

				// Clear metadata that changes on each sync
				// TODO: This should be mutators
				for _, obj := range []*unstructured.Unstructured{existingCopy, newCopy} {
					obj.SetResourceVersion("")
					obj.SetUID("")
					obj.SetCreationTimestamp(metav1.Time{})
					obj.SetManagedFields(nil)
					obj.SetGeneration(0)

					// Remove KCP-specific annotations and labels that are added automatically
					annotations := obj.GetAnnotations()
					if annotations != nil {
						delete(annotations, "kcp.io/cluster")
						if len(annotations) == 0 {
							obj.SetAnnotations(nil)
						} else {
							obj.SetAnnotations(annotations)
						}
					}

					labels := obj.GetLabels()
					if labels != nil {
						for key := range labels {
							if strings.HasPrefix(key, "claimed.internal.apis.kcp.io/") {
								delete(labels, key)
							}
						}
						if len(labels) == 0 {
							obj.SetLabels(nil)
						} else {
							obj.SetLabels(labels)
						}
					}
				}

				if cmp.Equal(existingCopy.Object, newCopy.Object) {
					logger.V(4).Info("No changes detected, skipping update", "object", consumerObj.GetName())
					return ctrl.Result{}, nil
				} else {
					logger.Info("Changes detected, updating consumer object", "object", consumerObj.GetName())
					logger.Info("Diff", "diff", cmp.Diff(existingCopy.Object, newCopy.Object))
				}

				// Preserve resource version and UID for update
				consumerObj.SetResourceVersion(existingObj.GetResourceVersion())
				consumerObj.SetUID(existingObj.GetUID())
				if err := cl.GetClient().Update(ctx, consumerObj); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to update consumer object in consumer cluster %s: %w", req.ClusterName, err)
				}
				logger.Info("Updated existing consumer object", "object", consumerObj.GetName())
			} else {
				return ctrl.Result{}, fmt.Errorf("failed to create consumer object in consumer cluster %s: %w", req.ClusterName, err)
			}
		} else {
			logger.Info("Created new consumer object", "object", consumerObj.GetName())
		}
	}

	logger.Info("Successfully synced related object to consumer cluster", "cluster", req.ClusterName)

	return ctrl.Result{}, nil
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

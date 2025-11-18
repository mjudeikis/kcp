package syncer

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/davecgh/go-spew/spew"
)

const (
	controllerName = "syncer-controller"
)

// Reconciler reconciles a custom resource to the provider cluster.
type Reconciler struct {
	manager    mcmanager.Manager
	opts       controller.TypedOptions[mcreconcile.Request]
	reconciler reconciler
}

// NewClusterBindingReconciler returns a new ClusterBindingReconciler to reconcile ClusterBindings.
func NewReconciler(
	_ context.Context,

	mgr mcmanager.Manager,
	opts controller.TypedOptions[mcreconcile.Request],
) (*Reconciler, error) {
	r := &Reconciler{
		manager:    mgr,
		opts:       opts,
		reconciler: reconciler{},
	}

	return r, nil
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling", "request", req)

	cl, err := r.manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get client for cluster %q: %w", req.ClusterName, err)
	}

	_ = cl.GetClient()
	_ = cl.GetCache()

	spew.Dump("Reconciling ClusterBinding", req.NamespacedName)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	return mcbuilder.ControllerManagedBy(mgr).
		//	For(&kubebindv1alpha2.ClusterBinding{}).
		//	Owns(&rbacv1.ClusterRole{}).
		//	Owns(&rbacv1.ClusterRoleBinding{}).
		//	Owns(&rbacv1.RoleBinding{}).
		WithOptions(r.opts).
		Named(controllerName).
		Complete(r)
}

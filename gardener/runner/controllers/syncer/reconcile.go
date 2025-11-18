package syncer

import (
	"context"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type reconciler struct {
}

func (r *reconciler) reconcile(ctx context.Context, client client.Client, cache cache.Cache) error {
	var errs []error

	return utilerrors.NewAggregate(errs)
}

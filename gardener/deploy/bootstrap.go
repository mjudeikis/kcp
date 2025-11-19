package kcp

import (
	"context"
	"time"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	confighelpers "github.com/kcp-dev/kcp/gardener/bootstrap/config/helpers"
	resources "github.com/kcp-dev/kcp/gardener/bootstrap/config/kcp/kcp"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	kcpclient "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/logicalcluster/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

var (
	// GardenerRootClusterName is the workspace to host common APIs.
	GardenerRootClusterName = logicalcluster.NewPath("root:gardener")
)

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(
	ctx context.Context,
	kcpClientSet kcpclientset.ClusterInterface,
	apiExtensionClusterClient kcpapiextensionsclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	batteriesIncluded sets.Set[string],
	opts confighelpers.Option,
) error {
	computeDiscoveryClient := apiExtensionClusterClient.Cluster(GardenerRootClusterName).Discovery()
	computeDynamicClient := dynamicClusterClient.Cluster(GardenerRootClusterName)

	crdClient := apiExtensionClusterClient.ApiextensionsV1().Cluster(GardenerRootClusterName).CustomResourceDefinitions()
	kcpClient := kcpClientSet.Cluster(GardenerRootClusterName)

	err := resources.Bootstrap(ctx, kcpClientSet, computeDiscoveryClient, computeDynamicClient, crdClient, batteriesIncluded, opts)
	if err != nil {
		return err
	}

	// create recursive apibinding so we can start controllers.
	// this is a temporary solution until we have a better way to bootstrap controllers.
	return bindAPIExport(ctx, kcpClient, "core.gardener.cloud", GardenerRootClusterName)
}

func bindAPIExport(ctx context.Context, kcpClient kcpclient.Interface, exportName string, clusterPath logicalcluster.Path) error {
	logger := klog.FromContext(ctx)

	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: clusterPath.String(),
					Name: exportName,
				},
			},
		},
	}

	binding.Spec.PermissionClaims = []apisv1alpha2.AcceptablePermissionClaim{}

	_, err := kcpClient.ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}

	if err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		existing, err := kcpClient.ApisV1alpha2().APIBindings().Get(ctx, exportName, metav1.GetOptions{})
		if err != nil {
			logger.Error(err, "error getting APIBinding", "name", exportName)
			// Always keep trying. Don't ever return an error out of this function.
			return false, nil
		}

		logger.V(2).Info("Updating API binding")
		existing.Spec = binding.Spec

		_, err = kcpClient.ApisV1alpha2().APIBindings().Update(ctx, existing, metav1.UpdateOptions{})
		if err == nil {
			return true, nil
		}
		if apierrors.IsConflict(err) {
			logger.V(2).Info("API binding update conflict, retrying")
			return false, nil
		}

		logger.Error(err, "error updating APIBinding")
		// Always keep trying. Don't ever return an error out of this function.
		return false, nil
	}); err != nil {
		return err
	}

	return nil
}

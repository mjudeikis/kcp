package core

import (
	"context"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/logicalcluster/v3"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/kcp/gardener/bootstrap/config/core/resources"
)

var (
	// KubeBindRootClusterName is the workspace to host common APIs.
	KubeBindRootClusterName = logicalcluster.NewPath("root:gardener")
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
) error {
	computeDiscoveryClient := apiExtensionClusterClient.Cluster(KubeBindRootClusterName).Discovery()
	computeDynamicClient := dynamicClusterClient.Cluster(KubeBindRootClusterName)

	crdClient := apiExtensionClusterClient.ApiextensionsV1().Cluster(KubeBindRootClusterName).CustomResourceDefinitions()
	return resources.Bootstrap(ctx, kcpClientSet, computeDiscoveryClient, computeDynamicClient, crdClient, batteriesIncluded)
}

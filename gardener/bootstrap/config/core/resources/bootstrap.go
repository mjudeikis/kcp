package resources

import (
	"context"
	"embed"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	kcpclientcluster "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

//go:embed *.yaml
var kubeFS embed.FS

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(
	ctx context.Context,
	kcpClient kcpclientcluster.ClusterInterface,
	discoveryClient discovery.DiscoveryInterface,
	dynamicClient dynamic.Interface,
	crdClient apiextensionsv1.CustomResourceDefinitionInterface,
	batteriesIncluded sets.Set[string]) error {
	// create resources in core cluster
	return confighelpers.Bootstrap(ctx, discoveryClient, dynamicClient, batteriesIncluded, kubeFS, confighelpers.ReplaceOption())
}

package runner

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/gardener/runner/options"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/multicluster-provider/apiexport"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

type Config struct {
	Options *options.CompletedOptions

	ProviderClientConfig *rest.Config
	KcpClientConfig      *rest.Config
	DynamicClient        dynamic.Interface

	Provider multicluster.Provider
	Manager  mcmanager.Manager

	// Webhook server configuration
	WebhookServer *http.Server
	TLSConfig     *tls.Config
}

func NewConfig(options *options.CompletedOptions) (*Config, error) {
	config := &Config{
		Options: options,
	}

	// Use Gardener kubeconfig as the primary client config for webhook validation
	var err error
	if options.GardenerKubeConfig != "" {
		config.ProviderClientConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.GardenerKubeConfig},
			&clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, err
		}
		config.ProviderClientConfig = rest.CopyConfig(config.ProviderClientConfig)
		config.ProviderClientConfig = rest.AddUserAgent(config.ProviderClientConfig, "gardener-kcp-runner")

		if config.DynamicClient, err = dynamic.NewForConfig(config.ProviderClientConfig); err != nil {
			return nil, err
		}
	}

	if options.KCPKubeConfig != "" {
		// Fallback to KCP kubeconfig if Gardener config is not provided
		config.KcpClientConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.KCPKubeConfig},
			&clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, err
		}
		config.KcpClientConfig = rest.CopyConfig(config.KcpClientConfig)
		config.KcpClientConfig = rest.AddUserAgent(config.KcpClientConfig, "gardener-kcp-runner")

		if config.DynamicClient, err = dynamic.NewForConfig(config.KcpClientConfig); err != nil {
			return nil, err
		}
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("error adding client-go scheme: %w", err)
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("error adding apiextensions scheme: %w", err)
	}
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("error adding apis scheme: %w", err)
	}
	if err := apisv1alpha2.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("error adding apis scheme: %w", err)
	}

	restConfig := config.KcpClientConfig
	// We need to build new url
	restConfig, err = buildClusterRestConfig(restConfig, options.APIExportEndpointSliceClusterPath)
	if err != nil {
		return nil, fmt.Errorf("error building kcp cluster rest config: %w", err)
	}

	provider, err := apiexport.New(restConfig, options.APIExportEndpointSliceName, apiexport.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("error setting up kcp provider: %w", err)
	}

	opts := ctrl.Options{
		Controller: ctrlconfig.Controller{},
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		Scheme: scheme,
	}

	manager, err := mcmanager.New(config.KcpClientConfig, provider, opts)
	if err != nil {
		return nil, fmt.Errorf("error setting up controller manager: %w", err)
	}

	config.Manager = manager

	return config, nil
}

func buildClusterRestConfig(baseConfig *rest.Config, clusterPath string) (*rest.Config, error) {
	restConfig := rest.CopyConfig(baseConfig)

	u, err := url.Parse(baseConfig.Host)
	if err != nil {
		return nil, err
	}
	u.Path = fmt.Sprintf("/clusters/%s", clusterPath)
	restConfig.Host = u.String()
	return restConfig, nil
}

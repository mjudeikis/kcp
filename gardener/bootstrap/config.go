package bootstrap

import (
	"net/url"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpclusterclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/gardener/bootstrap/options"
)

type Config struct {
	Options *options.CompletedOptions

	ClientConfig         *rest.Config
	KcpClusterClient     kcpclusterclientset.ClusterInterface
	ApiextensionsClient  kcpapiextensionsclientset.ClusterInterface
	DynamicClusterClient kcpdynamic.ClusterInterface
}

func NewConfig(options *options.CompletedOptions) (*Config, error) {
	config := &Config{
		Options: options,
	}

	kcpClientConfigOverrides := &clientcmd.ConfigOverrides{
		CurrentContext: options.KCPContext,
	}
	var err error
	config.ClientConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.KCPKubeConfig},
		kcpClientConfigOverrides).ClientConfig()
	if err != nil {
		return nil, err
	}
	config.ClientConfig = rest.CopyConfig(config.ClientConfig)
	config.ClientConfig = rest.AddUserAgent(config.ClientConfig, "gardener-kcp-init")

	config.ClientConfig, err = newKCPRestConfig(config.ClientConfig)
	if err != nil {
		return nil, err
	}

	if config.KcpClusterClient, err = kcpclusterclientset.NewForConfig(config.ClientConfig); err != nil {
		return nil, err
	}
	if config.ApiextensionsClient, err = kcpapiextensionsclientset.NewForConfig(config.ClientConfig); err != nil {
		return nil, err
	}
	if config.DynamicClusterClient, err = kcpdynamic.NewForConfig(config.ClientConfig); err != nil {
		return nil, err
	}

	return config, nil
}

func newKCPRestConfig(restConfig *rest.Config) (*rest.Config, error) {
	clusterConfig := rest.CopyConfig(restConfig)
	u, err := url.Parse(restConfig.Host)
	if err != nil {
		return nil, err
	}
	u.Path = ""
	clusterConfig.Host = u.String()
	clusterConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	return clusterConfig, nil
}

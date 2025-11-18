package runner

import (
	"crypto/tls"
	"net/http"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/gardener/runner/options"
)

type Config struct {
	Options *options.CompletedOptions

	ClientConfig  *rest.Config
	DynamicClient dynamic.Interface

	// Webhook server configuration
	WebhookServer *http.Server
	TLSConfig     *tls.Config
}

func NewConfig(options *options.CompletedOptions) (*Config, error) {
	config := &Config{
		Options: options,
	}

	// For now, we'll create minimal client configuration
	// This can be expanded later when we need actual KCP client connections
	var err error
	if options.GardenerKubeConfig != "" {
		config.ClientConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.GardenerKubeConfig},
			&clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, err
		}
		config.ClientConfig = rest.CopyConfig(config.ClientConfig)
		config.ClientConfig = rest.AddUserAgent(config.ClientConfig, "gardener-kcp-runner")

		if config.DynamicClient, err = dynamic.NewForConfig(config.ClientConfig); err != nil {
			return nil, err
		}
	}

	return config, nil
}

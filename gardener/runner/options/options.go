package options

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
)

type Options struct {
	Logs *logs.Options

	Serve *Serve

	ExtraOptions
}
type ExtraOptions struct {
	// APIExportEndpointSliceName is the name of the APIExportEndpointSlice to watch from the provider
	APIExportEndpointSliceName string
	// APIExportEndpointSliceClusterPath is the cluster path where the APIExportEndpointSlice is located
	APIExportEndpointSliceClusterPath string
	// KCP Kubeconfig path
	KCPKubeConfig string
	// Gardener Kubeconfig path
	GardenerKubeConfig string
}

type completedOptions struct {
	Logs  *logs.Options
	Serve *Serve

	ExtraOptions
}

type CompletedOptions struct {
	*completedOptions
}

func NewOptions() *Options {
	// Default to -v=2
	logs := logs.NewOptions()
	logs.Verbosity = logsv1.VerbosityLevel(2)

	return &Options{
		Logs:         logs,
		Serve:        NewServe(),
		ExtraOptions: ExtraOptions{},
	}
}

func (options *Options) AddFlags(fs *pflag.FlagSet) {
	logsv1.AddFlags(options.Logs, fs)
	options.Serve.AddFlags(fs)

	fs.StringVar(&options.GardenerKubeConfig, "gardener-kubeconfig", options.GardenerKubeConfig, "path to a gardener kubeconfig. Required to run the server.")
	fs.StringVar(&options.APIExportEndpointSliceName, "apiexport-endpointslice", "core.gardener.cloud", "Set the APIExportEndpointSlice name to watch from the provider")
	fs.StringVar(&options.APIExportEndpointSliceClusterPath, "apiexport-endpointslice-cluster-path", "root:gardener", "Set the cluster path where the APIExportEndpointSlice is located")
	fs.StringVar(&options.KCPKubeConfig, "kcp-kubeconfig", options.KCPKubeConfig, "path to a kcp kubeconfig. Required to run the server.")

}

func (options *Options) Complete() (*CompletedOptions, error) {
	err := options.Serve.Complete()
	if err != nil {
		return nil, err
	}

	return &CompletedOptions{
		completedOptions: &completedOptions{
			Logs:         options.Logs,
			Serve:        options.Serve,
			ExtraOptions: options.ExtraOptions,
		},
	}, nil
}

func (options *Options) Validate() error {
	if err := options.Serve.Validate(); err != nil {
		return err
	}

	if options.GardenerKubeConfig == "" || options.KCPKubeConfig == "" {
		return fmt.Errorf("both --gardener-kubeconfig and --kcp-kubeconfig must be provided")
	}

	// If gardener kubeconfig is provided, validate it exists
	if options.GardenerKubeConfig != "" {
		if _, err := os.Stat(options.GardenerKubeConfig); os.IsNotExist(err) {
			return fmt.Errorf("gardener kubeconfig file does not exist: %s", options.GardenerKubeConfig)
		}
	}

	if options.KCPKubeConfig != "" {
		if _, err := os.Stat(options.KCPKubeConfig); os.IsNotExist(err) {
			return fmt.Errorf("kcp kubeconfig file does not exist: %s", options.KCPKubeConfig)
		}
	}

	return nil
}

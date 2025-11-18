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

	ExtraOptions
}
type ExtraOptions struct {
	Port int

	CertFile string
	KeyFile  string

	GardenerKubeConfig string
}

type completedOptions struct {
	Logs *logs.Options

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
		ExtraOptions: ExtraOptions{},
	}
}

func (options *Options) AddFlags(fs *pflag.FlagSet) {
	logsv1.AddFlags(options.Logs, fs)

	fs.IntVar(&options.Port, "port", 9443, "Webhook server port")
	fs.StringVar(&options.CertFile, "tls-cert-file", options.CertFile, "File containing the x509 Certificate for HTTPS. (CA cert, if any, concatenated after server cert).")
	fs.StringVar(&options.KeyFile, "tls-private-key-file", options.KeyFile, "File containing the x509 private key matching --tls-cert-file.")
	fs.StringVar(&options.GardenerKubeConfig, "gardener-kubeconfig", options.GardenerKubeConfig, "path to a gardener kubeconfig. Required to run the server.")
}

func (options *Options) Complete() (*CompletedOptions, error) {
	// Set default values if not provided
	if options.Port == 0 {
		options.Port = 9443
	}

	// Validate certificate files are both provided or both empty
	if (options.CertFile == "") != (options.KeyFile == "") {
		return nil, fmt.Errorf("both --tls-cert-file and --tls-private-key-file must be provided together, or both omitted for insecure HTTP mode")
	}

	return &CompletedOptions{
		completedOptions: &completedOptions{
			Logs:         options.Logs,
			ExtraOptions: options.ExtraOptions,
		},
	}, nil
}

func (options *Options) Validate() error {
	// Validate port range
	if options.Port <= 0 || options.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", options.Port)
	}

	// If TLS certificates are provided, validate they exist
	if options.CertFile != "" {
		if _, err := os.Stat(options.CertFile); os.IsNotExist(err) {
			return fmt.Errorf("TLS certificate file does not exist: %s", options.CertFile)
		}
	}

	if options.KeyFile != "" {
		if _, err := os.Stat(options.KeyFile); os.IsNotExist(err) {
			return fmt.Errorf("TLS private key file does not exist: %s", options.KeyFile)
		}
	}

	// If gardener kubeconfig is provided, validate it exists
	if options.GardenerKubeConfig != "" {
		if _, err := os.Stat(options.GardenerKubeConfig); os.IsNotExist(err) {
			return fmt.Errorf("gardener kubeconfig file does not exist: %s", options.GardenerKubeConfig)
		}
	}

	return nil
}

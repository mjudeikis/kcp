package options

import (
	"encoding/base64"
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
	KCPKubeConfig string
	KCPContext    string

	WebhookCACert string

	// WebhookCAertBundle is the content of the CA bundle file for the webhook server in base64 encoding.
	WebhookCACertBundle string
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

	fs.StringVar(&options.KCPKubeConfig, "kcp-kubeconfig", options.KCPKubeConfig, "path to a kcp kubeconfig. Required to bootstrap the server.")
	fs.StringVar(&options.KCPContext, "context", options.KCPContext, "Name of the context in the kcp kubeconfig file to use")
	fs.StringVar(&options.WebhookCACert, "webhook-ca-cert", options.WebhookCACert, "CA certificate for the webhook server")
}

func (options *Options) Complete() (*CompletedOptions, error) {
	// read webhook CA cert file
	data, err := os.ReadFile(options.WebhookCACert)
	if err != nil {
		return nil, fmt.Errorf("failed to read webhook CA certificate file %q: %w", options.WebhookCACert, err)
	}
	options.WebhookCACertBundle = base64.StdEncoding.EncodeToString(data)

	return &CompletedOptions{
		completedOptions: &completedOptions{
			Logs:         options.Logs,
			ExtraOptions: options.ExtraOptions,
		},
	}, nil
}

func (options *Options) Validate() error {
	if options.KCPKubeConfig == "" {
		return fmt.Errorf("kcp kubeconfig must be specified")
	}

	if options.WebhookCACert == "" {
		return fmt.Errorf("webhook CA certificate must be specified")
	}
	if _, err := os.Stat(options.KCPKubeConfig); os.IsNotExist(err) {
		return fmt.Errorf("kcp kubeconfig file %q does not exist", options.KCPKubeConfig)
	}
	if _, err := os.Stat(options.WebhookCACert); os.IsNotExist(err) {
		return fmt.Errorf("webhook CA certificate file %q does not exist", options.WebhookCACert)
	}

	return nil
}

package options

import (
	"fmt"
	"net"

	"github.com/spf13/pflag"
)

type Serve struct {
	ListenAddress     string
	CertFile, KeyFile string

	// Listener is used to pre-wire a port zero listener for testing.
	Listener net.Listener
}

func NewServe() *Serve {
	return &Serve{
		ListenAddress: "127.0.0.1:9443",
	}
}

func (options *Serve) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&options.ListenAddress, "listen-address", options.ListenAddress, "The address where the backend should be listening on, defaults to 127.0.0.1:9443.")
	fs.StringVar(&options.CertFile, "tls-cert-file", options.CertFile, "The TLS certificate file the webserver will use.")
	fs.StringVar(&options.KeyFile, "tls-key-file", options.KeyFile, "The TLS private key file the webserver will use.")
}

func (options *Serve) Complete() error {
	if options.Listener == nil {
		var err error
		addr := options.ListenAddress
		// We only support TCP4 for now to avoid dual stack complications in embedded OIDC server tests.
		options.Listener, err = net.Listen("tcp4", addr)
		if err != nil {
			return err
		}
	}
	return nil
}

func (options *Serve) Validate() error {
	if options.ListenAddress == "" {
		return fmt.Errorf("listen-address must be provided")
	}
	if options.CertFile == "" && options.KeyFile != "" {
		return fmt.Errorf("TLS key file cannot be specified without TLS cert file")
	}
	if options.CertFile != "" && options.KeyFile == "" {
		return fmt.Errorf("TLS cert file cannot be specified without TLS key file")
	}

	return nil
}

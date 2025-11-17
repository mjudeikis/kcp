package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/pflag"
	genericapiserver "k8s.io/apiserver/pkg/server"
	logsv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/klog/v2"

	bootstrap "github.com/kcp-dev/kcp/gardener/bootstrap"
	"github.com/kcp-dev/kcp/gardener/bootstrap/options"
)

func main() {
	ctx := genericapiserver.SetupSignalContext()
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	defer klog.Flush()

	options := options.NewOptions()
	options.AddFlags(pflag.CommandLine)
	pflag.Parse()

	logger := klog.FromContext(ctx)
	logger.Info("Bootstrapping api")

	// setup logging first
	if err := logsv1.ValidateAndApply(options.Logs, nil); err != nil {
		return err
	}
	// create init server
	err := options.Validate()
	if err != nil {
		return err
	}

	// create init server
	completed, err := options.Complete()
	if err != nil {
		return err
	}

	// start server
	config, err := bootstrap.NewConfig(completed)
	if err != nil {
		return err
	}

	server, err := bootstrap.NewServer(ctx, config)
	if err != nil {
		return err
	}

	return server.Start(ctx)
}

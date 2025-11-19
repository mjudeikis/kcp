package main

import (
	"context"
	"fmt"
	"os"

	"github.com/kcp-dev/kcp/gardener/runner"
	"github.com/kcp-dev/kcp/gardener/runner/options"
	"github.com/spf13/pflag"
	genericapiserver "k8s.io/apiserver/pkg/server"
	logsv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
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
	logger.Info("Gardener-kcp runner")

	// setup logging first
	if err := logsv1.ValidateAndApply(options.Logs, nil); err != nil {
		return err
	}

	// Set up controller-runtime logger early to avoid warnings
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	log.SetLogger(klog.NewKlogr())

	err := options.Validate()
	if err != nil {
		return err
	}

	completed, err := options.Complete()
	if err != nil {
		return err
	}

	// start server
	config, err := runner.NewConfig(completed)
	if err != nil {
		return err
	}

	// Server is webhook server to proxy request for dry-run validation
	server, err := runner.NewServer(ctx, config)
	if err != nil {
		return err
	}

	return server.Start(ctx)
}

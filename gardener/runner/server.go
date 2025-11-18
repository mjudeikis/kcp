/*
Copyright 2025 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package runner

import (
	"context"
	"fmt"

	"github.com/kcp-dev/kcp/gardener/runner/controllers/syncer"
	"github.com/kcp-dev/kcp/gardener/runner/server"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

type Server struct {
	Config *Config

	WebhookServer *server.Server

	SyncerController *syncer.Reconciler
}

func NewServer(ctx context.Context, config *Config) (*Server, error) {
	s := &Server{
		Config: config,
	}
	// Webhook server to validate dry-run requests.
	webhookServer, err := server.NewServer(ctx, config.ProviderClientConfig, config.Options.Serve)
	if err != nil {
		return nil, err
	}
	s.WebhookServer = webhookServer

	// Controllers to do the actual syncing

	opts := controller.TypedOptions[mcreconcile.Request]{}

	s.SyncerController, err = syncer.NewReconciler(
		ctx,
		config.Manager,
		opts,
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up ClusterBinding Controller: %w", err)
	}

	// Register the ServiceExportRequest controller with the manager
	if err := s.SyncerController.SetupWithManager(s.Config.Manager); err != nil {
		return nil, fmt.Errorf("error setting up Syncer controller with manager: %w", err)
	}

	return s, nil

}

func (s *Server) Start(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	// start controller-runtime manager after bootstrap completes
	go func() {
		if err := s.Config.Manager.Start(ctx); err != nil {
			logger.Error(err, "Failed to start controller manager")
		}
	}()

	go func() {
		<-ctx.Done()
		logger.Info("Context done")
	}()
	return s.WebhookServer.Start(ctx)
}

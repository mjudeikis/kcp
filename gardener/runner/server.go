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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
)

type Server struct {
	Config *Config

	WebhookServer *server.Server

	SyncerController mccontroller.Controller
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

	// create the sync controller;
	// use the reconciler's log without any additional reconciling context
	syncController, err := syncer.Create(
		// This can be the reconciling context, as it's only used to find the target CRD during setup;
		// this context *must not* be stored in the sync controller!
		ctx,
		config.ProviderManager,
		config.ConsumerManager,
		schema.GroupVersionKind{
			Group:   "core.gardener.cloud",
			Version: "v1beta1",
			Kind:    "Shoot",
		},
		"gardener-syncer",
		klog.FromContext(ctx),
		1,
	)
	if err != nil {
		return nil, fmt.Errorf("error creating Syncer controller: %w", err)
	}
	s.SyncerController = syncController

	return s, nil

}

func (s *Server) Start(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	go func() {
		logger.Info("Starting sync unmanaged controller manager")
		if err := s.SyncerController.Start(ctx); err != nil {
			logger.Error(err, "Failed to start sync unmanaged controller manager")
		}
	}()
	// start controller-runtime manager after bootstrap completes
	go func() {
		logger.Info("Starting provider controller manager")
		if err := s.Config.ProviderManager.Start(ctx); err != nil {
			logger.Error(err, "Failed to start provider controller manager")
		}
	}()

	go func() {
		logger.Info("Starting consumer controller manager")
		if err := s.Config.ConsumerManager.Start(ctx); err != nil {
			logger.Error(err, "Failed to start consumer controller manager")
		}
	}()

	go func() {
		<-ctx.Done()
		logger.Info("Context done")
	}()
	return s.WebhookServer.Start(ctx)
}

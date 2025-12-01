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
	"time"

	"github.com/kcp-dev/kcp/gardener/runner/controllers/related"
	"github.com/kcp-dev/kcp/gardener/runner/controllers/syncer"
	"github.com/kcp-dev/kcp/gardener/runner/predicates"
	"github.com/kcp-dev/kcp/gardener/runner/server"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicclient "k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/klog/v2"
)

type Server struct {
	Config *Config

	WebhookServer *server.Server
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

	// Controllers to do the actual syncing;

	// relevant relatedGVK to sync from provider to consumer if they are owned by syncer objects.
	relatedGVKs := []schema.GroupVersionKind{
		{
			Group:   "",
			Version: "v1",
			Kind:    "Secret",
		},
	}

	shootGVK := schema.GroupVersionKind{
		Group:   "core.gardener.cloud",
		Version: "v1beta1",
		Kind:    "Shoot",
	}

	predicatesRegistry := predicates.New()

	// create the sync controller;
	// use the reconciler's log without any additional reconciling context
	syncController, err := syncer.Create(
		// This can be the reconciling context, as it's only used to find the target CRD during setup;
		// this context *must not* be stored in the sync controller!
		ctx,
		config.ProviderManager,
		config.ConsumerManager,
		shootGVK,
		predicatesRegistry,
		klog.FromContext(ctx),
		1,
	)
	if err != nil {
		return nil, fmt.Errorf("error creating Syncer controller: %w", err)
	}

	dynamicProviderClient := dynamicclient.NewForConfigOrDie(config.ProviderClientConfig)
	defaultProviderInf := dynamicinformer.NewDynamicSharedInformerFactory(dynamicProviderClient, time.Minute*30)

	for _, gvk := range relatedGVKs {
		klog.Infof("Syncer will sync related GVK: %s", gvk.String())
		relatedSyncer, err := related.Create(
			ctx,
			config.ProviderManager,
			config.ConsumerManager,
			defaultProviderInf,
			shootGVK,
			gvk,
			predicatesRegistry,
			klog.FromContext(ctx),
			1,
		)
		if err != nil {
			return nil, fmt.Errorf("error creating Related Syncer controller for GVK %s: %w", gvk.String(), err)
		}
		err = config.ConsumerManager.Add(relatedSyncer)
		if err != nil {
			return nil, fmt.Errorf("error adding Related Syncer controller for GVK %s to ConsumerManager: %w", gvk.String(), err)
		}
	}

	err = s.Config.ConsumerManager.Add(syncController)
	if err != nil {
		return nil, fmt.Errorf("error adding Syncer controller to ConsumerManager: %w", err)
	}

	return s, nil

}

func (s *Server) Start(ctx context.Context) error {
	logger := klog.FromContext(ctx)

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

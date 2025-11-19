/*
Copyright 2025 The Kube Bind Authors.

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

package bootstrap

import (
	"bytes"
	"context"

	confighelpers "github.com/kcp-dev/kcp/gardener/bootstrap/config/helpers"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	bootstrapconfig "github.com/kcp-dev/kcp/gardener/bootstrap/config/config"
	bootstrapcore "github.com/kcp-dev/kcp/gardener/bootstrap/config/core"
	bootstrapkcp "github.com/kcp-dev/kcp/gardener/bootstrap/config/kcp"
)

type Server struct {
	Config *Config
}

func NewServer(ctx context.Context, config *Config) (*Server, error) {
	s := &Server{
		Config: config,
	}

	return s, nil
}

func (s *Server) Start(ctx context.Context) error {
	fakeBatteries := sets.New[string]()
	logger := klog.FromContext(ctx)

	if err := bootstrapconfig.Bootstrap(
		ctx,
		s.Config.KcpClusterClient,
		s.Config.ApiextensionsClient,
		s.Config.DynamicClusterClient,
		fakeBatteries,
	); err != nil {
		logger.Error(err, "failed to bootstrap initial config workspace")
		return nil // don't klog.Fatal. This only happens when context is cancelled.
	}

	if err := bootstrapcore.Bootstrap(
		ctx,
		s.Config.KcpClusterClient,
		s.Config.ApiextensionsClient,
		s.Config.DynamicClusterClient,
		fakeBatteries,
	); err != nil {
		logger.Error(err, "failed to bootstrap core workspace")
		return nil // don't klog.Fatal. This only happens when context is cancelled.
	}

	opts := confighelpers.Option{
		TransformFile: func(bs []byte) ([]byte, error) {
			// replace the placeholder with the actual CA bundle
			return bytes.ReplaceAll(bs, []byte("CA_BUNDLE_PLACEHOLDER"), []byte(s.Config.Options.WebhookCACertBundle)), nil
		},
	}

	if err := bootstrapkcp.Bootstrap(
		ctx,
		s.Config.KcpClusterClient,
		s.Config.ApiextensionsClient,
		s.Config.DynamicClusterClient,
		fakeBatteries,
		opts,
	); err != nil {
		logger.Error(err, "failed to bootstrap workspace")
		return nil // don't klog.Fatal. This only happens when context is cancelled.
	}

	return nil
}

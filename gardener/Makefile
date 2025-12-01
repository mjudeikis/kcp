# Copyright 2025 The KCP Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

SHELL := /usr/bin/env bash -e

# Basic build configuration
GO_INSTALL = ./hack/go-install.sh
GOBIN_DIR=$(abspath ./bin)
PATH := $(GOBIN_DIR):$(PATH)

# KCP specific configuration
KCP_VER := v0.28.3
KCP_BIN := kcp
KCP := $(GOBIN_DIR)/$(KCP_BIN)-$(KCP_VER)
KCP_CMD ?= $(KCP)

# Gardener configuration
GARDENER_KUBECONFIG ?= $(error GARDENER_KUBECONFIG is not set. Please set it to the path of your Gardener kubeconfig file)

ARCH := $(shell go env GOARCH)
OS := $(shell go env GOOS)

all: build
.PHONY: all

build: ## Build the gardener integration binaries
	mkdir -p $(GOBIN_DIR)
	GOOS=$(OS) GOARCH=$(ARCH) CGO_ENABLED=0 go build -o $(GOBIN_DIR)/gardener-init ./cmd/init/
	GOOS=$(OS) GOARCH=$(ARCH) CGO_ENABLED=0 go build -o $(GOBIN_DIR)/gardener-runner ./cmd/runner/
.PHONY: build

install: ## Install binaries to GOBIN
	go install ./cmd/init/
	go install ./cmd/runner/
.PHONY: install

test: ## Run unit tests
	go test -race -count 1 ./...
.PHONY: test

check-gardener-config: ## Validate that GARDENER_KUBECONFIG is set and accessible
	@echo "Checking Gardener configuration..."
	@if [ -z "$(GARDENER_KUBECONFIG)" ]; then \
		echo "Error: GARDENER_KUBECONFIG environment variable is not set."; \
		echo "Please set it to the path of your Gardener kubeconfig file:"; \
		echo "  export GARDENER_KUBECONFIG=/path/to/your/gardener/kubeconfig"; \
		echo "  make runner"; \
		exit 1; \
	fi
	@if [ ! -f "$(GARDENER_KUBECONFIG)" ]; then \
		echo "Error: Gardener kubeconfig file not found at: $(GARDENER_KUBECONFIG)"; \
		echo "Please ensure the file exists and the path is correct."; \
		exit 1; \
	fi
	@echo "✓ Gardener kubeconfig found at: $(GARDENER_KUBECONFIG)"
.PHONY: check-gardener-config

$(KCP):
	mkdir -p $(GOBIN_DIR)
	curl --fail --retry 3 -L "https://github.com/kcp-dev/kcp/releases/download/$(KCP_VER)/kcp_$(KCP_VER:v%=%)_$(OS)_$(ARCH).tar.gz" | \
	tar xz -C "$(GOBIN_DIR)" --strip-components="1" bin/kcp
	mv $(GOBIN_DIR)/kcp $(KCP)

run-kcp: $(KCP) ## Run KCP server for development
	mkdir -p .kcp
	$(KCP_CMD) start --bind-address=127.0.0.1

setup-kcp: $(KCP) ## Setup KCP infrastructure (blocking - run in background)
	mkdir -p .kcp
	$(KCP_CMD) start --bind-address=127.0.0.1 &>.kcp/kcp.log & KCP_PID=$$!; \
	trap 'kill -TERM $$KCP_PID; rm -rf .kcp' TERM INT EXIT && \
	echo "Waiting for kcp to be ready (check .kcp/kcp.log)." && \
	while ! KUBECONFIG=.kcp/admin.kubeconfig kubectl get --raw /readyz &>/dev/null; do sleep 1; echo -n "."; done && echo && \
	echo "KCP is ready. Press Ctrl+C to stop." && \
	wait $$KCP_PID

localhost.pem: ## Generate TLS certificate for webhook server (only if not exists)
	@if [ -f localhost.pem ]; then \
		echo "✓ TLS certificate already exists: localhost.pem"; \
	elif command -v openssl >/dev/null 2>&1; then \
		echo "Generating TLS certificate using openssl..."; \
		openssl req -x509 -newkey rsa:2048 -keyout localhost.pem -out localhost.pem -days 365 -nodes \
			-subj "/C=US/ST=CA/L=San Francisco/O=KCP/CN=localhost" \
			-addext "subjectAltName=DNS:localhost,IP:127.0.0.1"; \
		echo "Certificate generated: localhost.pem"; \
	elif go version >/dev/null 2>&1; then \
		echo "Installing genkey tool..."; \
		go install github.com/mjudeikis/genkey@latest; \
		echo "Generating TLS certificate using genkey..."; \
		genkey localhost; \
		echo "Certificate generated: localhost.pem"; \
	else \
		echo "Error: Neither openssl nor go found. Please install one of them."; \
		exit 1; \
	fi

certs: localhost.pem ## Ensure TLS certificates are available
.PHONY: certs

init: build certs ## Initialize KCP workspaces and API exports
	./bin/gardener-init \
		--kcp-kubeconfig .kcp/admin.kubeconfig \
		--webhook-ca-cert localhost.pem

runner: build certs check-gardener-config ## Run the gardener-kcp runner (requires TLS certs and kubeconfigs)
	./bin/gardener-runner \
		--tls-cert-file=localhost.pem \
		--tls-key-file=localhost.pem \
		--gardener-kubeconfig=$(GARDENER_KUBECONFIG) \
		--kcp-kubeconfig=.kcp/admin.kubeconfig -v 2

clean: ## Clean build artifacts and generated files
	rm -rf $(GOBIN_DIR)
	rm -rf .kcp
	rm -f localhost.pem
.PHONY: clean

help: ## Show this help
	@awk 'BEGIN { fs="## " } { FS=fs } /:.*##/ { doc=$$2; FS=":"; $$0=$$0; printf "\033[36m%-20s\033[0m %s\n", $$1, doc; }' $(MAKEFILE_LIST) | sort
.PHONY: help
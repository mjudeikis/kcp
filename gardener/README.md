# Gardener Integration with kcp

This is a Proof of Concept (POC) implementation that integrates Gardener with kcp, enabling Gardener resources to be managed through kcp's multicluster architecture.

## Overview

The gardener directory contains a complete implementation for syncing Gardener Shoot resources between kcp consumer workspaces and Gardener provider clusters. This allows users to manage Kubernetes clusters through kcp while leveraging Gardener's cluster provisioning capabilities.

## Architecture

The implementation consists of three main components:

### 1. Bootstrap System (`bootstrap/`)
- **Purpose**: Initializes kcp workspaces and API resources for Gardener integration
- **Key files**:
  - `bootstrap/server.go`: Main bootstrap server that sets up workspace hierarchy
  - `bootstrap/config/`: Configuration for different workspace types (config, core, kcp)
  - `bootstrap/options/`: Command-line options for bootstrap process

### 2. Runner System (`runner/`)
- **Purpose**: Provides webhook validation and resource synchronization between kcp and Gardener
- **Key components**:
  - `runner/server.go`: Main server orchestrating webhook server and controllers
  - `runner/controllers/syncer/`: Primary controller syncing Shoot resources between clusters
  - `runner/controllers/related/`: Controller syncing related resources (e.g., Secrets)
  - `runner/mutators/`: Object transformation logic for consumer ↔ provider conversions. Should be reused across controllers.
  - `runner/predicates/`: Dynamic filtering predicates store for related resource controllers

### 3. Command Line Tools (`cmd/`)
- **`cmd/init/main.go`**: Bootstrap command for setting up kcp workspaces and API resources
- **`cmd/runner/main.go`**: Main runner process for webhook server and synchronization

## Core Functionality

### Resource Synchronization
The system implements bidirectional synchronization between kcp consumer workspaces and Gardener provider clusters:

**Spec Synchronization (Consumer → Provider):**
- Copies Shoot specifications from kcp consumer workspaces to Gardener clusters
- Applies mutations to transform kcp format to Gardener-compatible format
- Preserves certain provider-managed fields (`spec.cloudProfile`, `spec.region`, etc.)

**Status Synchronization (Provider → Consumer):**
- Syncs Shoot status from Gardener clusters back to kcp consumer workspaces
- Updates consumer objects with provider cluster state

### Mutation System
The mutation system (`runner/mutators/mutators.go`) handles format transformations:

- **`ShootToProvider()`**: Transforms consumer Shoot objects for provider clusters
- **`ShootToConsumer()`**: Transforms provider Shoot objects for consumer clusters
- **Preserved fields**: Maintains provider-specific configurations that shouldn't be overwritten

### Related Resource Management
The system automatically syncs related resources (currently Secrets) that are owned by Shoot resources:

- Uses dynamic predicates to filter resources based on ownership
- Maintains consistency between consumer and provider clusters for dependent resources

## Deployment Structure (`deploy/`)

### CRDs (`deploy/crds/`)
- **`core.gardener.cloud_shoots.yaml`**: Shoot resource definition
- **`core.gardener.cloud_secretbindings.yaml`**: SecretBinding resource definition

### kcp Resources (`deploy/kcp/`)
- **APIResourceSchemas**: Define how Gardener resources are exposed in kcp
- **APIExport**: Exports Gardener resources for consumption by other workspaces
- **Validation policies**: Admission control for Gardener resources in kcp

## Technical Details

### Multicluster Runtime Integration
The system leverages [multicluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime) for cross-cluster operations:

- **Consumer Manager**: Manages kcp consumer workspaces as source clusters
- **Provider Manager**: Manages Gardener clusters as target/provider clusters
- **Cross-cluster watches**: Monitors resources across multiple clusters simultaneously

### Webhook Server (`runner/server/`)
Provides admission webhook functionality for Gardener resources:

- **Validation**: Ensures Shoot resources conform to provider requirements
- **Mutation**: Applies necessary transformations during admission
- **TLS Support**: Requires TLS certificates for secure webhook communication

### Deletion Handling
Gardener resources require special deletion handling:

```go
// From syncer_controller.go:324-339
// Delete is 2 step process. Add annotation: confirmation.gardener.cloud/deletion=true to config, then delete.
// Without the annotation, Gardener will not delete the object, even if deleteTimestamp is set.
```

The system automatically adds the required `confirmation.gardener.cloud/deletion=true` annotation before deletion.

### Predicate Registry
Dynamic filtering system for related resources:

- Registers/deregisters predicates based on object lifecycle
- Filters resources based on ownership relationships
- Prevents unnecessary reconciliation of unrelated resources

## Known Limitations and TODOs

Based on the current implementation (`TODO.md`), several areas need attention:

1. **Schema Validation**: Gardener schemas lack `x-kubernetes-preserve-unknown-fields: true` on object fields, causing validation errors in kcp's stricter validation
2. **Generation Tracking**: Gardener doesn't always bump generation numbers, potentially causing missed updates in watch-based synchronization
3. **Label Selectors**: Need label-based filtering for owned Secrets instead of wildcard listing
4. **Deletion Flow**: Requires annotation-based deletion confirmation for Gardener resources

## Build System

The project includes a `Makefile` with standard Go build targets and integrates with the broader kcp build system via `go.work` workspace configuration.

## Setup and Usage

### Prerequisites
- kcp cluster running with admin access
- Gardener cluster/environment set up locally
- Go 1.24+ for building components
- kubectl configured for both kcp and Gardener access

### Quick Start

For a complete setup with Make targets:
```bash
# Set your Gardener kubeconfig path
export GARDENER_KUBECONFIG=/path/to/your/gardener/kubeconfig

# Start KCP (in background)
make setup-kcp &

# Wait for KCP to be ready, then initialize and run
make init && make runner
```

### Step-by-Step Setup

#### 1. Setup Local Gardener Environment
Follow the official Gardener local setup guide:
```bash
# See: https://github.com/gardener/gardener/blob/master/docs/development/local_setup.md
```

#### 2. Prepare kcp Assets and CRDs
Extract Gardener CRDs for kcp integration:
```bash
go run ./cmd/crd-puller pull-crds \
    --resources shoots,secretbindings \
    --kubeconfig ~/Downloads/kubeconfig-garden-kcp.yaml \
    --output-dir gardener/deploy/crds
```

#### 3. Generate kcp API Resources
Create APIResourceSchema and APIExport for Gardener resources:
```bash
go install github.com/kcp-dev/sdk/cmd/apigen
apigen --input-dir deploy/crds --output-dir deploy/kcp
```

> **Note**: Current schemas need manual addition of `x-kubernetes-preserve-unknown-fields: true` on object type fields to prevent kcp validation errors.

#### 4. Set Gardener Configuration
Configure the path to your Gardener kubeconfig:
```bash
export GARDENER_KUBECONFIG=/path/to/your/gardener/kubeconfig
```

#### 5. Start kcp Infrastructure
Choose one of the following options:

**Option A: Interactive Mode (foreground)**
```bash
make run-kcp
```

**Option B: Background Mode with Logging**
```bash
make setup-kcp
```

#### 6. Initialize kcp Workspaces
Bootstrap the necessary workspaces and API exports (auto-generates TLS certificates):
```bash
make init
```

#### 7. Start the Gardener-kcp Runner
Launch the webhook server and synchronization controllers:
```bash
make runner
```

#### 8. Create Consumer Workspace and Test
Set up a consumer workspace and create test resources:
```bash
export KUBECONFIG=.kcp/admin.kubeconfig

# Create and enter consumer workspace
kubectl kcp ws use :root
kubectl kcp ws create gardener-consumer --enter

# Bind to Gardener API export
kubectl kcp bind apiexport root:gardener:core.gardener.cloud \
    --accept-permission-claim secrets.core

# Create test resources
kubectl create namespace garden-local
kubectl create -f examples/shoot.yaml
```

### Available Make Targets

- **`make help`** - Show all available targets
- **`make build`** - Build binaries
- **`make certs`** - Generate TLS certificates (if not present)
- **`make run-kcp`** - Start kcp server (foreground)
- **`make setup-kcp`** - Start kcp server with logging (background)
- **`make init`** - Initialize kcp workspaces and API exports
- **`make runner`** - Run the gardener-kcp integration server
- **`make check-gardener-config`** - Validate Gardener configuration
- **`make clean`** - Clean all generated files and build artifacts
- **`make test`** - Run unit tests

## Resource Flow and Mutations

The system uses server-side apply patching for mutations. The Gardener runner watches for changes in Shoot resources and applies transformations during synchronization between consumer and provider clusters.

### Mutation Process

1. **Consumer → Provider**: `ShootToProvider()` transforms kcp consumer objects to Gardener-compatible format
2. **Provider → Consumer**: `ShootToConsumer()` transforms Gardener objects back to kcp consumer format
3. **Preserved Fields**: Certain provider-managed fields are preserved during mutations to prevent conflicts

### Preserved Fields During Mutation
The following fields are maintained by the provider and not overwritten during consumer updates:

```go
// From mutators.go:52-59
var preserveToProvider = []string{
    "metadata.finalizers",
    "spec.cloudProfile", 
    "spec.region",
    "spec.dns",
    "spec.networking.pods",
    "spec.networking.services",
}
```

### Example Resource Transformation

Here's an example Shoot resource as it appears in kcp after creation but BEFORE Gardener reconciliation:

```yaml
apiVersion: core.gardener.cloud/v1beta1
kind: Shoot
metadata:
  annotations:
    authentication.gardener.cloud/issuer: managed
    kcp.io/cluster: 37beed3hkjo0u5ua
    shoot.gardener.cloud/cloud-config-execution-max-delay-seconds: "0"
  creationTimestamp: "2025-11-28T09:07:15Z"
  generation: 1
  labels:
    networking.extensions.gardener.cloud/calico: "true"
    operatingsystemconfig.extensions.gardener.cloud/local: "true"
    provider.extensions.gardener.cloud/local: "true"
  name: local2
  namespace: garden-local
  resourceVersion: "21334"
  uid: 3774e5ae-454a-40da-9d46-d38d68a63977
spec:
  addons:
    kubernetesDashboard:
      authenticationMode: token
      enabled: false
  cloudProfile:
    kind: CloudProfile
    name: local
  credentialsBindingName: local
  kubernetes:
    kubeAPIServer:
      defaultNotReadyTolerationSeconds: 300
      defaultUnreachableTolerationSeconds: 300
      eventTTL: 1h0m0s
      logging:
        verbosity: 2
      requests:
        maxMutatingInflight: 200
        maxNonMutatingInflight: 400
    kubeControllerManager:
      nodeCIDRMaskSize: 24
      nodeMonitorGracePeriod: 40s
    kubeProxy:
      enabled: true
      mode: IPTables
    kubeScheduler:
      profile: balanced
    kubelet:
      failSwapOn: true
      imageGCHighThresholdPercent: 50
      imageGCLowThresholdPercent: 40
      imageMaximumGCAge: 0s
      imageMinimumGCAge: 2m0s
      kubeReserved:
        cpu: 80m
        memory: 1Gi
        pid: 20k
      protectKernelDefaults: true
      registryBurst: 20
      registryPullQPS: 10
      seccompDefault: true
      serializeImagePulls: false
      streamingConnectionIdleTimeout: 5m
    version: 1.34.0
    verticalPodAutoscaler:
      cpuHistogramDecayHalfLife: 24h0m0s
      enabled: true
      evictAfterOOMThreshold: 10m0s
      evictionRateBurst: 1
      evictionRateLimit: -1
      evictionTolerance: 0.5
      memoryAggregationInterval: 24h0m0s
      memoryAggregationIntervalCount: 8
      memoryHistogramDecayHalfLife: 24h0m0s
      recommendationLowerBoundCPUPercentile: 0.5
      recommendationLowerBoundMemoryPercentile: 0.5
      recommendationMarginFraction: 0.15
      recommendationUpperBoundCPUPercentile: 0.95
      recommendationUpperBoundMemoryPercentile: 0.95
      recommenderInterval: 1m0s
      targetCPUPercentile: 0.9
      targetMemoryPercentile: 0.9
      updaterInterval: 1m0s
  maintenance:
    autoUpdate:
      kubernetesVersion: true
      machineImageVersion: true
    timeWindow:
      begin: 210000+0000
      end: 220000+0000
  networking:
    ipFamilies:
    - IPv4
    nodes: 10.0.0.0/32
    type: calico
  provider:
    type: local
    workers:
    - cri:
        name: containerd
      machine:
        architecture: amd64
        image:
          name: local
          version: 1.0.0
        type: local
      maxSurge: 1
      maxUnavailable: 0
      maximum: 2
      minimum: 1
      name: local
      systemComponents:
        allow: true
      updateStrategy: AutoRollingUpdate
    workersSettings:
      sshAccess:
        enabled: true
  purpose: evaluation
  region: local
  schedulerName: default-scheduler
  systemComponents:
    coreDNS:
      autoscaling:
        mode: horizontal
```

After successful reconciliation, the status section will be populated with Gardener cluster state, and preserved fields in the spec may be updated by the provider.

## Development

### Project Structure
```
gardener/
├── bootstrap/          # kcp workspace initialization
│   ├── config/         # Workspace configurations
│   └── options/        # CLI options
├── cmd/               # Command-line tools
│   ├── init/          # Bootstrap command
│   └── runner/        # Main runner process
├── deploy/            # Deployment resources
│   ├── crds/          # Gardener CRDs
│   └── kcp/           # kcp-specific resources
├── examples/          # Example resources
└── runner/            # Core runtime components
    ├── controllers/   # Synchronization controllers
    ├── mutators/      # Object transformation logic
    ├── predicates/    # Dynamic filtering
    └── server/        # Webhook server
```

### Key Interfaces

**Mutator Interface (`runner/mutators/mutators.go:26-29`)**:
```go
type Mutator struct {
    ToProvider func(consumer, provider *unstructured.Unstructured) error
    ToConsumer func(provider, consumer *unstructured.Unstructured) error
}
```

**Controller Integration**: Uses multicluster-runtime for cross-cluster resource management with standard controller-runtime patterns.

### Testing
Run tests using standard Go tooling:
```bash
go test ./...
```

For integration testing, ensure both kcp and Gardener environments are properly configured.

## Contributing

This is a POC implementation. Areas for contribution include:

1. **Schema Improvements**: Add proper `x-kubernetes-preserve-unknown-fields` support
2. **Enhanced Filtering**: Implement label-based filtering for related resources  
3. **Generation Tracking**: Improve handling of resources that don't bump generation
4. **Error Handling**: Enhanced retry logic and error reporting
5. **Documentation**: Additional examples and use cases
6. **Testing**: Comprehensive integration test suite

## License

This project follows the same license as the parent kcp project (Apache 2.0).
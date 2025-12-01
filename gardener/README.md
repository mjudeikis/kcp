# Gardener Poc

Raw notes from Gardener POC



1. Setup local gardener environment: https://github.com/gardener/gardener/blob/master/docs/development/local_setup.md


2. We need to prepare kcp assets/crds to be used in kcp.

```bash
 go run ./cmd/crd-puller pull-crds --resources shoots,secretbindings --kubeconfig ~/Downloads/kubeconfig-garden-kcp.yaml  --output-dir gardener/deploy/crds
```

3. Generate APIResourceSchema and APIExport for shoot resource in kcp.

```yaml
go install github.com/kcp-dev/sdk/cmd/apigen 
apigen --input-dir deploy/crds --output-dir deploy/kcp 
```

TODO: Schema needs `x-kubernetes-preserve-unknown-fields: true` of object type fields to prevent 
validation errors in kcp. Need to fix apigen or crd-puller to add that automatically.z

This should produce gardener shoot crd in gardener directory.

5. We will need cert key as admision only allows tls:

```bash
go install github.com/mjudeikis/genkey
genkey localhost 
```

3. Create an apiexport & apiresource schemas for shoot resource in kcp.

```yaml
make build
go run ./cmd/init/main.go --kcp-kubeconfig ../.kcp/admin.kubeconfig --webhook-ca-cert ../localhost.pem
```

6. Start fake admission server:

```bash
 go run cmd/runner/main.go \
    --tls-cert-file=../localhost.pem \
    --tls-key-file=../localhost.pem \
    --gardener-kubeconfig=/Users/mjudeikis/go/src/github.com/gardener/gardener/example/gardener-local/kind/local/kubeconfig \
    --kcp-kubeconfig=../.kcp/admin.kubeconfig -v 2
```

7. Create a consumer workspace and bind it to gardener provider workspace:

```bash
export KUBECONFIG=../.kcp/admin.kubeconfig
k ws use :root 
k ws create gardener-consumer --enter

kubectl kcp bind apiexport root:gardener:core.gardener.cloud --accept-permission-claim secrets.core

k create namespace garden-local
k create -f examples/shoot.yaml  
```

## Mutations

Mutations are applied using server side apply patching. Gardener runner watches for changes in shoot resources and applies mutations accordingly.

Example shoot.yaml example after creation BEFORE reconciliation:

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
# Gardener Poc

Raw notes from Gardener POC



1. Setup local gardener environment: https://github.com/gardener/gardener/blob/master/docs/development/local_setup.md


2. We need to prepare kcp assets/crds to be used in kcp.

```bash
 go run ./cmd/crd-puller pull-crds --resources shoots --kubeconfig ~/Downloads/kubeconfig-garden-kcp.yaml  --output-dir gardener/deploy/crds
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

kubectl kcp bind apiexport root:gardener:core.gardener.cloud

k create namespace garden-local
k create -f examples/shoot.yaml  
```





# AGENTS.md

## Project Overview

KEDA HTTP Add-on — a Kubernetes add-on that autoscales HTTP workloads based on incoming traffic. It extends KEDA by tracking pending HTTP request queue sizes and reporting scaling metrics. Written in Go 1.19.

## Build Commands

```bash
make build                  # Build all three binaries (operator, interceptor, scaler)
make build-operator         # Build operator only
make build-interceptor      # Build interceptor only
make build-scaler           # Build scaler only

make test                   # Run go fmt, go vet, then go test ./...
go test ./operator/...      # Test operator package only
go test ./pkg/queue/...     # Test a specific package
go test -run TestName ./pkg/queue/...  # Run a single test

make fmt                    # go fmt ./...
make vet                    # go vet ./...
make pre-commit             # Run all static checks (golangci-lint, formatting, etc.)

make generate               # Run codegen + manifests (required after changing CRD types)
make proto-gen              # Regenerate gRPC protobuf files from proto/scaler.proto

make docker-build           # Build all three Docker images
make deploy                 # Deploy to current kubectl cluster via Kustomize
make e2e-test               # Run end-to-end tests (requires running K8s cluster + KEDA)
```

## Architecture

Three independent components communicate through Kubernetes resources and internal HTTP/gRPC endpoints:

### Operator (`operator/`)
- Watches for `HTTPScaledObject` CRDs and reconciles internal resources
- Updates a routing table ConfigMap that maps hostnames to backend services
- Creates KEDA `ScaledObject` resources for each `HTTPScaledObject`
- Entry point: `operator/main.go`; controller: `operator/controllers/http/httpscaledobject_controller.go`
- CRD types defined in `operator/apis/http/v1alpha1/httpscaledobject_types.go`

### Interceptor (`interceptor/`)
- Accepts incoming HTTP traffic and routes it to the correct backend service using the routing table
- Tracks pending request count per host (requests forwarded but not yet responded)
- Exposes queue metrics to the scaler via an admin HTTP endpoint
- Waits for deployments to scale up before forwarding (scale-from-zero)
- Entry point: `interceptor/main.go`; proxy logic in `proxy_handlers.go`, `middleware.go`, `request_forwarder.go`

### Scaler (`scaler/`)
- Implements KEDA's External Scaler gRPC interface (defined in `proto/scaler.proto`)
- Periodically pings interceptors to aggregate pending request counts
- Reports metrics to KEDA which feeds them to HPA for scaling decisions
- Entry point: `scaler/main.go`; gRPC handlers in `handlers.go`; interceptor polling in `queue_pinger.go`

### Shared Packages (`pkg/`)
- `pkg/routing/` — Thread-safe routing table (hostname → service target), JSON-serializable, stored in ConfigMap
- `pkg/queue/` — In-memory pending request counter with postponed resize support (prevents premature scale-down)
- `pkg/k8s/` — Kubernetes client helpers and informer utilities
- `pkg/env/` — Environment variable parsing
- `pkg/net/` — Network utilities (exponential backoff dialing)

## Data Flow

1. User creates `HTTPScaledObject` CR → Operator updates routing table ConfigMap + creates KEDA `ScaledObject`
2. HTTP request arrives → Interceptor looks up host in routing table, increments pending counter, forwards request to backend service, decrements counter on response
3. Scaler periodically fetches pending counts from interceptors via HTTP → reports to KEDA via gRPC → KEDA drives HPA scaling

## Code Generation

After modifying CRD types in `operator/apis/http/v1alpha1/`, you must run:
```bash
make generate    # Regenerates DeepCopy methods and CRD/RBAC manifests
```

After modifying `proto/scaler.proto`:
```bash
make proto-gen   # Regenerates Go gRPC stubs
```

## Linting

Uses golangci-lint with many linters enabled (see `.golangci.yml`). Import ordering enforced by `gci`: standard library, then third-party, then `github.com/kedacore/http-add-on` prefix. Test files are exempt from `dupl` and `unparam` linters.

## Testing

- Unit tests use `testify/require` and Ginkgo v2 / Gomega
- E2E tests (`tests/e2e-test.sh`) require a Kind cluster with KEDA installed
- CI runs on both amd64 and arm64; tests against Kubernetes 1.24–1.26

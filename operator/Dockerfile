# Build the manager binary
FROM --platform=$BUILDPLATFORM ghcr.io/kedacore/build-tools:1.19.5 as builder
ARG VERSION=main
ARG GIT_COMMIT=HEAD

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY operator operator
COPY pkg pkg
COPY proto proto
COPY Makefile Makefile

# Build
# the ARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
ARG TARGETOS
ARG TARGETARCH
RUN VERSION=${VERSION} GIT_COMMIT=${GIT_COMMIT} TARGET_OS=${TARGETOS:-linux} ARCH=${TARGETARCH} make build-operator

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/operator .
USER 65532:65532

ENTRYPOINT ["/operator"]

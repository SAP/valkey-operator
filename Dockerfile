# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.25.6 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the go module manifests
COPY go.mod go.mod
COPY go.sum go.sum
# Cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go sources
COPY main.go main.go
COPY api/ api/
COPY pkg/ pkg/
COPY crds/ crds/
COPY internal/ internal/
COPY tests/ tests/
COPY Makefile Makefile

# Run tests and build
RUN make envtest \
 && CGO_ENABLED=0 KUBEBUILDER_ASSETS="/workspace/bin/k8s/current" go test ./... \
 && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]

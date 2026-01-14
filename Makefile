##@ License

# Set the year for SPDX header updates (default: current year)
YEAR ?= $(shell date +%Y)

.PHONY: update-year-in-headers
update-year-in-headers: ## Update SPDX year in file headers to $(YEAR)
    # Go + TXT + MD
	@find . -type f \( -name "*.go" -o -name "*.txt" -o -name "*.md" \) -exec sed -i \
    's/^SPDX-FileCopyrightText: [0-9]\{4\}\( SAP SE or an SAP affiliate company and [^"]\+ contributors\)/SPDX-FileCopyrightText: $(YEAR)\1/' {} +

    # TOML
	@find . -type f -name "*.toml" -exec sed -i \
    's/^SPDX-FileCopyrightText = "[0-9]\{4\}\( SAP SE or an SAP affiliate company and [^"]\+ contributors\)"/SPDX-FileCopyrightText = "$(YEAR)\1"/' {} +
# Image URL to use all building/pushing image targets
IMG ?= valkey-operator:latest
# K8s version used by envtest
ENVTEST_K8S_VERSION = 1.30.3

# Set shell to bash
SHELL = /usr/bin/env bash
.SHELLFLAGS = -o pipefail -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate CustomResourceDefinition objects
	$(LOCALBIN)/controller-gen crd paths="./api/..." output:crd:artifacts:config=crds && \
	test ! -d chart || test -e chart/crds || ln -s ../crds chart/crds

.PHONY: generate
generate: generate-deepcopy ## Generate required code pieces

.PHONY: generate-deepcopy
generate-deepcopy: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations
	$(LOCALBIN)/controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: generate-client
generate-client: ## Generate typed client
	./hack/genclient.sh

.PHONY: fmt
fmt: ## Run go fmt against code
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code
	go vet ./...

##@ Testing

.PHONY: test
test: manifests generate-deepcopy fmt vet envtest ## Run tests
	KUBEBUILDER_ASSETS="$(LOCALBIN)/k8s/current" go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: generate-deepcopy fmt vet ## Build manager binary
	go build -o bin/manager main.go

.PHONY: run
run: manifests generate-deepcopy fmt vet ## Run a controller from your host
	go run ./main.go

# Build docker image in current architecture and tag it as ${IMG}
.PHONY: docker-build
docker-build: ## Build docker image with the manager
	docker build -t ${IMG} .

# Push docker image to the target specified in ${IMG}
.PHONY: docker-push
docker-push: ## Push docker image with the manager
	docker push ${IMG}

# Build and push docker image for all given platforms
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	- docker buildx create --name project-v3-builder
	docker buildx use project-v3-builder
	- docker buildx build --push --platform=$(PLATFORMS) --tag ${IMG} .
	- docker buildx rm project-v3-builder

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	@mkdir -p $(LOCALBIN)

.PHONY: controller-gen
controller-gen: $(LOCALBIN) ## Install controller-gen
	@go mod download sigs.k8s.io/controller-tools && \
	VERSION=$$(go list -m -f '{{.Version}}' sigs.k8s.io/controller-tools) && \
	if [ ! -L $(LOCALBIN)/controller-gen ] || [ "$$(readlink $(LOCALBIN)/controller-gen)" != "controller-gen-$$VERSION" ]; then \
	echo "Installing controller-gen $$VERSION" && \
	rm -f $(LOCALBIN)/controller-gen && \
	GOBIN=$(LOCALBIN) go install $$(go list -m -f '{{.Dir}}' sigs.k8s.io/controller-tools)/cmd/controller-gen && \
	mv $(LOCALBIN)/controller-gen $(LOCALBIN)/controller-gen-$$VERSION && \
	ln -s controller-gen-$$VERSION $(LOCALBIN)/controller-gen; \
	fi

.PHONY: setup-envtest
setup-envtest: $(LOCALBIN) ## Install setup-envtest
	@go mod download sigs.k8s.io/controller-runtime/tools/setup-envtest && \
	VERSION=$$(go list -m -f '{{.Version}}' sigs.k8s.io/controller-runtime/tools/setup-envtest) && \
	if [ ! -L $(LOCALBIN)/setup-envtest ] || [ "$$(readlink $(LOCALBIN)/setup-envtest)" != "setup-envtest-$$VERSION" ]; then \
	echo "Installing setup-envtest $$VERSION" && \
	rm -f $(LOCALBIN)/setup-envtest && \
	GOBIN=$(LOCALBIN) go install $$(go list -m -f '{{.Dir}}' sigs.k8s.io/controller-runtime/tools/setup-envtest) && \
	mv $(LOCALBIN)/setup-envtest $(LOCALBIN)/setup-envtest-$$VERSION && \
	ln -s setup-envtest-$$VERSION $(LOCALBIN)/setup-envtest; \
	fi

.PHONY: envtest
envtest: setup-envtest ## Install envtest binaries
	@ENVTESTDIR=$$($(LOCALBIN)/setup-envtest use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path) && \
	chmod -R u+w $$ENVTESTDIR && \
	rm -f $(LOCALBIN)/k8s/current && \
	ln -s $$ENVTESTDIR $(LOCALBIN)/k8s/current

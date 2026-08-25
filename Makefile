# =============================================================================
# lightspeed-hub — Makefile
# =============================================================================
# Development and build targets following the same patterns as
# lightspeed-operator and lightspeed-agentic-operator.
# =============================================================================

VERSION ?= latest

IMG ?= lightspeed-hub-operator:$(VERSION)
export IMG

# -----------------------------------------------------------------------------
# Tool paths
# -----------------------------------------------------------------------------
ifeq (,$(shell go env GOBIN))
GOBIN := $(shell go env GOPATH)/bin
else
GOBIN := $(shell go env GOBIN)
endif

CONTAINER_TOOL ?= "$(shell which podman >/dev/null 2>&1 && echo podman || echo docker)"

LOCALBIN ?= $(shell pwd)/bin

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

OPERATOR_NAMESPACE ?= openshift-lightspeed

METRICS_BIND_ADDRESS ?= :18080
HEALTH_PROBE_BIND_ADDRESS ?= :18081

CONTROLLER_GEN_VERSION ?= v0.21.0
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

GOLANGCI_LINT_VERSION ?= v2.9.0
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint

KUSTOMIZE_VERSION ?= v5.3.0
KUSTOMIZE ?= $(LOCALBIN)/kustomize

KUBECTL ?= kubectl

ENVTEST_VERSION ?= $(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' sigs.k8s.io/controller-runtime 2>/dev/null)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' k8s.io/api 2>/dev/null | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')
ENVTEST ?= $(LOCALBIN)/setup-envtest

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: fmt-check
fmt-check: ## Verify go fmt produces no changes.
	@test -z "$$(gofmt -l .)" || { echo "Unformatted files:"; gofmt -l .; exit 1; }

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: fmt-check vet setup-envtest ## Run unit tests.
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out -count=1

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter.
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes.
	$(GOLANGCI_LINT) run --fix

.PHONY: manifests
manifests: controller-gen ## Regenerate CRD YAML and RBAC ClusterRole.
	$(CONTROLLER_GEN) rbac:roleName=hub-operator-manager-role crd paths="./..." output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

##@ Build

.PHONY: build
build: fmt vet ## Build manager binary.
	go build -o bin/manager ./cmd

.PHONY: run
run: install vet ## Run the controller locally.
	go run ./cmd/main.go \
		--namespace=$(OPERATOR_NAMESPACE) \
		--metrics-bind-address=$(METRICS_BIND_ADDRESS) \
		--health-probe-bind-address=$(HEALTH_PROBE_BIND_ADDRESS)

.PHONY: docker-build
docker-build: ## Build container image.
	@test -f Dockerfile || (echo "error: no Dockerfile in repo root" >&2; exit 1)
	$(CONTAINER_TOOL) build -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push container image.
	$(CONTAINER_TOOL) push $(IMG)

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the cluster.
	@out="$$( $(KUSTOMIZE) build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | $(KUBECTL) apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Remove CRDs from the cluster.
	@out="$$( $(KUSTOMIZE) build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the cluster.
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Remove controller from the cluster.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Build Dependencies

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	@if test -x $(CONTROLLER_GEN) && ! $(CONTROLLER_GEN) --version 2>&1 | grep -q $(CONTROLLER_GEN_VERSION); then \
		echo "$(CONTROLLER_GEN) version is not expected $(CONTROLLER_GEN_VERSION). Removing it before installing."; \
		rm -rf $(CONTROLLER_GEN); \
	fi
	test -s $(CONTROLLER_GEN) || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || GOBIN=$(LOCALBIN) go install -mod=mod sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

.PHONY: setup-envtest
setup-envtest: envtest ## Download the envtest binaries.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(ENVTEST) || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	@if test -x $(GOLANGCI_LINT) && ! $(GOLANGCI_LINT) --version 2>&1 | grep -q $(GOLANGCI_LINT_VERSION); then \
		echo "$(GOLANGCI_LINT) version is not expected $(GOLANGCI_LINT_VERSION). Removing it before installing."; \
		rm -rf $(GOLANGCI_LINT); \
	fi
	test -s $(GOLANGCI_LINT) || GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

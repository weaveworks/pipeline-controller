
# Image URL to use all building/pushing image targets
IMG_TAG ?= $(shell echo "$$(git describe --tags "$$(git rev-parse "HEAD^{commit}")^{commit}" --match v* 2>/dev/null || git rev-parse "HEAD^{commit}")$$([ -z "$$(git status --porcelain 2>/dev/null)" ] || echo -dirty)")
IMG_REGISTRY ?= ghcr.io
IMG ?= $(IMG_REGISTRY)/weaveworks/pipeline-controller:$(IMG_TAG)
GIT_REVISION ?= $(shell echo $$(git rev-parse "HEAD^{commit}")$$([ -z "$$(git status --porcelain 2>/dev/null)" ] || echo -dirty))
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.24.2
CHART_REGISTRY ?= ghcr.io/weaveworks/charts
CHART_PATH ?= $(shell pwd)/charts/pipeline-controller
SED ?= /usr/bin/sed

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif
# GOPRIVATE = github.com/weaveworks/cluster-controller

DOCKER_BUILD_ARGS ?= --load
DOCKER_BUILD_LABELS ?= --label org.opencontainers.image.created=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ") \
	--label org.opencontainers.image.authors="Weaveworks Product Engineering" \
	--label org.opencontainers.image.source=github.com/weaveworks/pipeline-controller \
	--label org.opencontainers.image.version=$(IMG_TAG) \
	--label org.opencontainers.image.revision=$(GIT_REVISION) \
	--label org.opencontainers.image.vendor=Weaveworks

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=pipeline-controller crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

lint: golangci-lint ## Run linters against code
	$(GOLANGCI_LINT) run --out-format=github-actions --timeout 600s

.PHONY: tidy
tidy:
	go mod tidy
	(cd api && go mod tidy)

.PHONY: verify-tidy
verify-tidy: tidy
	git diff --exit-code -- go.mod go.sum api/go.mod api/go.sum

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test ./... -coverprofile cover.out

.PHONY: lint-chart
lint-chart:
	ct lint --config ct.yaml --target-branch main

.PHONY: helm-chart
helm-chart: APP_VERSION=$(shell echo "$$(git describe --tags "$$(git rev-parse "HEAD^{commit}")^{commit}" --match v* 2>/dev/null || git rev-parse "HEAD^{commit}")$$([ -z "$$(git status --porcelain 2>/dev/null)" ] || echo -dirty)")
helm-chart:
	$(SED) -i 's/tag: latest/tag: ${APP_VERSION}/' $(CHART_PATH)/values.yaml
	$(HELM) package $(CHART_PATH) --app-version=${APP_VERSION} --debug
##@ Build

.PHONY: build
build: generate fmt vet ## Build manager binary.
	go build -o bin/manager main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./main.go

.PHONY: docker-build
docker-build: test ## Build docker image with the manager.
	docker buildx build --secret id=netrc,src=.netrc -t ${IMG} $(DOCKER_BUILD_ARGS) $(DOCKER_BUILD_LABELS) .

.PHONY: docker-push
docker-push: DOCKER_BUILD_ARGS=--push --platform linux/arm64/v8,linux/amd64
docker-push: docker-build

.PHONY: release
release: IMG_TAG=$(shell echo "$$(git describe --tags "$$(git rev-parse "HEAD^{commit}")^{commit}" --match v* 2>/dev/null || git rev-parse "HEAD^{commit}")$$([ -z "$$(git status --porcelain 2>/dev/null)" ] || echo -dirty)")
release: docker-push

.PHONY: helm-release
helm-release: VERSION=$(shell echo "$$(grep "version: "  ./charts/pipeline-controller/Chart.yaml | awk '{print $$2}')")
helm-release: kustomize helm helm-chart
	$(HELM) push pipeline-controller-${VERSION}.tgz oci://${CHART_REGISTRY}

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint
HELM ?= $(LOCALBIN)/helm

## Tool Versions
KUSTOMIZE_VERSION ?= v4.5.7
CONTROLLER_TOOLS_VERSION ?= v0.9.2
GOLANGCI_LINT_VERSION ?= v1.48.0
HELM_VERSION ?= v3.10.1

KUSTOMIZE_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	test -s $(LOCALBIN)/kustomize || { curl -s $(KUSTOMIZE_INSTALL_SCRIPT) | bash -s -- $(subst v,,$(KUSTOMIZE_VERSION)) $(LOCALBIN); }

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT): $(LOCALBIN)
	test -s $(LOCALBIN)/golangci-lint || GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: helm
helm: $(HELM)
$(HELM): $(LOCALBIN)
	test -s $(LOCALBIN)/helm || { curl -s https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | HELM_INSTALL_DIR=$(LOCALBIN) bash -s -- --no-sudo --version $(HELM_VERSION); }
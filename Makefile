#!/usr/bin/make -f
.PHONY: all
all: build

GO_BUILD_PACKAGES := ./cmd/... ./pkg/...
GO_TEST_PACKAGES := ./cmd/... ./pkg/...

CONTAINER_ENGINE ?= docker
IMAGE_REGISTRY ?= ghcr.io/gordon-code
IMAGE_TAG ?= latest
PLATFORM ?= linux/amd64

# renovate: datasource=github-releases depName=golangci/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.14.0
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: build
build:
	go build $(GO_BUILD_PACKAGES)
	go test -run=^$$ ./test/...

.PHONY: test
test:
	go test $(GO_TEST_PACKAGES)

.PHONY: test-staging
test-staging:
	RUN_LETSENCRYPT_STAGING_TESTS=1 go test -v -run TestLiveLetsEncryptStagingIntegration ./pkg/controller/route

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run ./cmd/... ./pkg/...

.PHONY: verify-gomod
verify-gomod:
	go mod tidy -diff
	go mod verify

.PHONY: verify-deploy-consistency
verify-deploy-consistency:
	hack/verify-deploy-consistency.sh

.PHONY: verify
verify: verify-gomod vet lint verify-deploy-consistency

.PHONY: image-controller
image-controller:
	$(CONTAINER_ENGINE) build --platform=$(PLATFORM) -f images/openshift-acme-controller/Dockerfile -t $(IMAGE_REGISTRY)/openshift-acme-controller:$(IMAGE_TAG) .

.PHONY: image-exposer
image-exposer:
	$(CONTAINER_ENGINE) build --platform=$(PLATFORM) -f images/openshift-acme-exposer/Dockerfile -t $(IMAGE_REGISTRY)/openshift-acme-exposer:$(IMAGE_TAG) .

.PHONY: images
images: image-controller image-exposer

test-e2e: export E2E_DOMAIN ?=$(shell oc get ingresses.config.openshift.io cluster --template='{{.spec.domain}}')
test-e2e: export E2E_CONTROLLER_NAMESPACE ?=acme-controller
test-e2e: export E2E_FIXED_NAMESPACE ?=
test-e2e: export E2E_ARGS :=-args -ginkgo.v
test-e2e: export E2E_JUNIT ?=
.PHONY: test-e2e
test-e2e:
	go test -v ./test/e2e/... $(E2E_ARGS) -ginkgo.junit-report=$(E2E_JUNIT)

.PHONY: ci-test-e2e-cluster-wide
ci-test-e2e-cluster-wide:
	$(MAKE) --no-print-directory test-e2e E2E_CONTROLLER_NAMESPACE:=acme-controller E2E_FIXED_NAMESPACE:=

.PHONY: ci-test-e2e-single-namespace
ci-test-e2e-single-namespace:
	$(MAKE) --no-print-directory test-e2e E2E_CONTROLLER_NAMESPACE:=acme-controller E2E_FIXED_NAMESPACE:=acme-controller

.PHONY: ci-test-e2e-specific-namespaces
ci-test-e2e-specific-namespaces:
	$(MAKE) --no-print-directory test-e2e E2E_CONTROLLER_NAMESPACE:=acme-controller E2E_FIXED_NAMESPACE:=acme-controller
	$(MAKE) --no-print-directory test-e2e E2E_CONTROLLER_NAMESPACE:=acme-controller E2E_FIXED_NAMESPACE:=test

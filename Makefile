# Build variables
VERSION ?= latest
REGISTRY ?= registry.cn-hangzhou.aliyuncs.com
IMAGE_NAME ?= alibabacloud-nlb-pool-operator
IMAGE_TAG ?= $(VERSION)
IMAGE = $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

# Go build variables
GO ?= go
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0
GOTOOLCHAIN ?= local

# Binary name
BINARY_NAME = manager

# Controller-gen version
CONTROLLER_GEN ?= $(shell pwd)/bin/controller-gen

.PHONY: all
all: build

.PHONY: build
build: fmt vet
	@echo "Building $(BINARY_NAME)..."
	GOTOOLCHAIN=$(GOTOOLCHAIN) CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build \
		-o bin/$(BINARY_NAME) \
		./main.go

.PHONY: run
run: fmt vet
	@echo "Running $(BINARY_NAME)..."
	GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) run ./main.go

.PHONY: fmt
fmt:
	@echo "Running go fmt..."
	GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) fmt ./...

.PHONY: vet
vet:
	@echo "Running go vet..."
	GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) vet ./...

.PHONY: test
test: fmt vet
	@echo "Running tests..."
	GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) test ./... -coverprofile cover.out

.PHONY: generate
generate: controller-gen
	@echo "Generating deepcopy..."
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./apis/..."

.PHONY: manifests
manifests: controller-gen
	@echo "Generating CRD and RBAC manifests..."
	$(CONTROLLER_GEN) crd rbac:roleName=nlb-pool-operator-role paths="./..." output:crd:dir=config/crd/bases output:rbac:dir=config/rbac

.PHONY: docker-build
docker-build:
	@echo "Building docker image $(IMAGE)..."
	docker build -t $(IMAGE) .

.PHONY: docker-push
docker-push:
	@echo "Pushing docker image $(IMAGE)..."
	docker push $(IMAGE)

.PHONY: controller-gen
controller-gen:
	@echo "Downloading controller-gen..."
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0)

.PHONY: clean
clean:
	@echo "Cleaning up..."
	rm -rf bin/

.PHONY: help
help:
	@echo "AlibabaCloud NLB Pool Operator Makefile Commands:"
	@echo "  make build         - Build the binary"
	@echo "  make run           - Run the operator locally"
	@echo "  make fmt           - Run go fmt"
	@echo "  make vet           - Run go vet"
	@echo "  make test          - Run tests"
	@echo "  make generate      - Generate deepcopy code"
	@echo "  make manifests     - Generate CRD manifests"
	@echo "  make docker-build  - Build docker image"
	@echo "  make docker-push   - Push docker image"
	@echo "  make clean         - Clean build artifacts"

# go-get-tool will 'go install' a tool with the given version
define go-get-tool
@[ -f "$(1)" ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$$(dirname "$(1)") go install "$(2)" ;\
rm -rf $$TMP_DIR ;\
}
endef

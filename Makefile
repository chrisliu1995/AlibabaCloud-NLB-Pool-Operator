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

# Controller-gen version. Must match the version recorded in the committed
# CRDs (controller-gen.kubebuilder.io/version annotation), otherwise
# 'make verify' reports drift on every run.
CONTROLLER_GEN ?= $(shell pwd)/bin/controller-gen
CONTROLLER_GEN_VERSION ?= v0.16.5

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

# verify regenerates all artifacts and fails when committed generated files
# (deepcopy, CRDs, RBAC) have drifted from the Go sources. Diff is scoped to
# generated paths only, so uncommitted source edits do not false-positive.
.PHONY: verify
verify: generate manifests
	@echo "Verifying generated artifacts are up to date..."
	@git diff --exit-code -- 'apis/**/zz_generated*.go' config/crd/bases config/rbac/role.yaml || \
		(echo "ERROR: generated artifacts drifted from source. Run 'make generate manifests' and commit the result." && exit 1)
	@echo "Generated artifacts are up to date."

# doctor checks every prerequisite needed by 'make run' and names each
# missing item explicitly so failures are diagnosable.
.PHONY: doctor
doctor:
	@echo "Checking prerequisites..."
	@ok=1; \
	if ! command -v $(GO) >/dev/null 2>&1; then echo "  [FAIL] go toolchain not found"; ok=0; \
	else echo "  [OK]   go: $$($(GO) version | cut -d' ' -f3)"; fi; \
	if [ -z "$$ACCESS_KEY_ID" ]; then echo "  [FAIL] env ACCESS_KEY_ID is not set"; ok=0; \
	else echo "  [OK]   env ACCESS_KEY_ID is set"; fi; \
	if [ -z "$$ACCESS_KEY_SECRET" ]; then echo "  [FAIL] env ACCESS_KEY_SECRET is not set"; ok=0; \
	else echo "  [OK]   env ACCESS_KEY_SECRET is set"; fi; \
	if [ -z "$$REGION_ID" ]; then echo "  [FAIL] env REGION_ID is not set"; ok=0; \
	else echo "  [OK]   env REGION_ID is set ($$REGION_ID)"; fi; \
	if ! command -v kubectl >/dev/null 2>&1; then echo "  [WARN] kubectl not found, skipping cluster checks"; \
	elif ! kubectl version --request-timeout=5s >/dev/null 2>&1; then echo "  [FAIL] kubeconfig cluster not reachable"; ok=0; \
	else echo "  [OK]   kubeconfig cluster reachable"; \
		for crd in nlbs.nlboperator.alibabacloud.com eips.eip.alibabacloud.com nlbpools.nlbpool.alibabacloud.com portallocations.nlbpool.alibabacloud.com; do \
			if kubectl get crd $$crd >/dev/null 2>&1; then echo "  [OK]   CRD $$crd registered"; \
			else echo "  [FAIL] CRD $$crd not registered (install NLB/EIP Operator or run make manifests + kubectl apply)"; ok=0; fi; \
		done; \
	fi; \
	if [ $$ok -eq 1 ]; then echo "All prerequisites satisfied."; else echo "Prerequisites missing, see [FAIL] items above."; exit 1; fi

# scan runs secret scanning (gitleaks) and dependency vulnerability
# scanning (govulncheck). Both tools are installed on demand.
.PHONY: scan
scan: scan-secrets scan-vulns

.PHONY: scan-secrets
scan-secrets:
	@echo "Running secret scan (gitleaks)..."
	@if command -v gitleaks >/dev/null 2>&1; then \
		gitleaks detect --source . --no-banner; \
	else \
		echo "gitleaks not installed; install via 'brew install gitleaks' or see https://github.com/gitleaks/gitleaks"; exit 1; \
	fi

.PHONY: scan-vulns
scan-vulns:
	@echo "Running dependency vulnerability scan (govulncheck)..."
	@GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

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
	@echo "Downloading controller-gen $(CONTROLLER_GEN_VERSION)..."
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION))

.PHONY: clean
clean:
	@echo "Cleaning up..."
	rm -rf bin/
	rm -f manager cover.out

.PHONY: help
help:
	@echo "AlibabaCloud NLB Pool Operator Makefile Commands:"
	@echo "  make build         - Build the binary"
	@echo "  make run           - Run the operator locally"
	@echo "  make fmt           - Run go fmt"
	@echo "  make vet           - Run go vet"
	@echo "  make test          - Run tests"
	@echo "  make verify        - Fail if generated artifacts drifted from source"
	@echo "  make doctor        - Check run prerequisites (go, cloud env vars, kubeconfig, CRDs)"
	@echo "  make scan          - Run secret (gitleaks) and vulnerability (govulncheck) scans"
	@echo "  make generate      - Generate deepcopy code"
	@echo "  make manifests     - Generate CRD manifests"
	@echo "  make docker-build  - Build docker image"
	@echo "  make docker-push   - Push docker image"
	@echo "  make clean         - Clean build artifacts (bin/, manager, cover.out)"

# go-get-tool will 'go install' a tool with the given version.
# GOTOOLCHAIN is pinned so the tool builds reproducibly regardless of the
# host Go version.
define go-get-tool
@[ -f "$(1)" ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d 2>/dev/null || mktemp -d -t tool) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOTOOLCHAIN=go1.23.4 GOBIN=$$(dirname "$(1)") go install "$(2)" ;\
rm -rf $$TMP_DIR ;\
}
endef

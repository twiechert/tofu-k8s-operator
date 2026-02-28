set shell := ["bash", "-euo", "pipefail", "-c"]

IMG := "tofu-k8s-operator:dev"
KIND_CLUSTER := "tofu-e2e"

# Install development tools (macOS)
tools-install:
    @if ! command -v brew >/dev/null 2>&1; then \
        echo 'Homebrew is required. Please install it from https://brew.sh/'; exit 1; \
    fi
    @brew install kind kubectl docker || true

# Build the operator binary
build:
    go mod tidy
    go mod download
    go build -o bin/manager main.go

# Build the kubectl-tofu plugin binary
build-plugin:
    go build -o bin/kubectl-tofu ./cmd/kubectl-tofu/

# Install the kubectl-tofu plugin to GOPATH/bin
install-plugin:
    go install ./cmd/kubectl-tofu/

# Build the Docker image
docker-build:
    docker build -t {{ IMG }} .

# Deploy manifests to the current cluster
deploy:
    kubectl apply -k deploy/

# Apply example resources
examples:
    kubectl apply -k examples/

# Run unit tests
test:
    go test ./...

# Run unit tests with coverage report
test-cover:
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html

# Create a Kind cluster for e2e tests
kind-up:
    @echo "[kind-up] Ensuring clean Kind cluster..."
    -kind delete cluster --name {{ KIND_CLUSTER }} >/dev/null 2>&1 || true
    kind create cluster --name {{ KIND_CLUSTER }}

# Delete the Kind e2e cluster
kind-down:
    kind delete cluster --name {{ KIND_CLUSTER }} || true

# Build and load the image into Kind
kind-load:
    docker build --no-cache -t {{ IMG }} .
    kind load docker-image {{ IMG }} --name {{ KIND_CLUSTER }}

# Run end-to-end tests (creates Kind cluster, builds & loads image, runs tests)
e2e: kind-up kind-load
    #!/usr/bin/env bash
    set -euo pipefail
    tmpfile=$(mktemp)
    kind get kubeconfig --name {{ KIND_CLUSTER }} > "$tmpfile"
    KUBECONFIG="$tmpfile" go test -v -tags=integration ./test/e2e
    ret=$?
    rm -f "$tmpfile"
    exit $ret

# Clean up e2e resources (deletes Kind cluster)
e2e-clean: kind-down

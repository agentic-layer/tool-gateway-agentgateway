# Tool Gateway kgateway Operator

The Tool Gateway kgateway Operator is a Kubernetes operator that manages `ToolGateway` instances based on [agentgateway/kgateway](https://github.com/agentgateway/kgateway). It provides centralized gateway management for tool workloads within the Agentic Layer ecosystem.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Getting Started](#getting-started)
- [Development](#development)
- [Configuration](#configuration)
- [End-to-End (E2E) Testing](#end-to-end-e2e-testing)
- [Testing Tools and Configuration](#testing-tools-and-configuration)
- [Sample Data](#sample-data)
- [Contribution](#contribution)

----

## Prerequisites

Before working with this project, ensure you have the following tools installed on your system:

* **Go**: version 1.24.0 or higher
* **Docker**: version 20.10+ (or a compatible alternative like Podman)
* **kubectl**: The Kubernetes command-line tool
* **kind**: For running Kubernetes locally in Docker
* **helm**: Helm 3+ for installing kgateway
* **make**: The build automation tool

----

## Getting Started

**Quick Start:**

```shell
# Create local cluster
kind create cluster

# Install cert-manager
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.1/cert-manager.yaml

# Install Gateway API CRDs
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/standard-install.yaml

# Install kgateway with agentgateway support
kubectl create ns agentgateway-system

helm upgrade -i kgateway-crds \
  oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-crds \
  --namespace agentgateway-system \
  --version v2.1.2

helm upgrade -i kgateway \
  oci://cr.kgateway.dev/kgateway-dev/charts/kgateway \
  --namespace agentgateway-system \
  --version v2.1.2

# Install the Tool Gateway operator
kubectl apply -f https://github.com/agentic-layer/tool-gateway-kgateway/releases/latest/download/install.yaml
```

## How it Works

The Tool Gateway kgateway Operator creates and manages Gateway API resources based on ToolGateway and ToolServer custom resources:

1. **Gateway Creation**: When a ToolGateway is created, the operator creates an `agentgateway-proxy` Gateway in the `agentgateway-system` namespace with HTTP listener on port 80.

2. **ToolServer Integration**: For each ToolServer resource, the operator creates:
   - **AgentgatewayBackend**: Configures the MCP backend connection to the ToolServer
   - **HTTPRoute**: Routes traffic from the gateway to the AgentgatewayBackend using path-based matching

3. **Automatic Updates**: The operator watches for changes to ToolGateway and ToolServer resources and updates the corresponding Gateway API resources automatically.

### Architecture

```
ToolGateway (CRD)
    ↓
agentgateway-proxy (Gateway)
    ↓
HTTPRoute (Gateway API) → AgentgatewayBackend → ToolServer
```

## Development

Follow the prerequisites above to set up your local environment.
Then follow these steps to build and deploy the operator locally:

```shell
# Install CRDs into the cluster
make install
# Build docker image
make docker-build
# Load image into kind cluster (not needed if using local registry)
make kind-load
# Deploy the operator to the cluster
make deploy
```

After a successful start, you should see the controller manager pod running in the `tool-gateway-kgateway-system` namespace.

```bash
kubectl get pods -n tool-gateway-kgateway-system
```

## Configuration

### Prerequisites for ToolGateway

Before creating a ToolGateway, ensure you have:

1. **Gateway API CRDs** installed in your cluster
2. **kgateway with agentgateway support** installed (see [Getting Started](#getting-started))
3. **ToolGatewayClass** with `agentgateway` controller (automatically created by this operator)

### ToolGateway Configuration

To create a kgateway-based gateway for your tools, define a `ToolGateway` resource:

```yaml
apiVersion: runtime.agentic-layer.ai/v1alpha1
kind: ToolGateway
metadata:
  name: my-tool-gateway
  namespace: my-namespace
spec:
  toolGatewayClassName: kgateway  # Optional: uses default if not specified
```

This will create an `agentgateway-proxy` Gateway in the `agentgateway-system` namespace.

### ToolServer Configuration

Define ToolServer resources that the gateway will route to:

```yaml
apiVersion: runtime.agentic-layer.ai/v1alpha1
kind: ToolServer
metadata:
  name: my-tool-server
  namespace: my-namespace
spec:
  protocol: mcp
  transportType: http
  image: my-tool-server:latest
  port: 8000
  path: /mcp
  replicas: 1
```

The operator will automatically create:
- An **AgentgatewayBackend** for the ToolServer
- An **HTTPRoute** connecting the Gateway to the backend

### Accessing Your Tools

Once deployed, tools are accessible via the agentgateway-proxy Gateway:

```shell
# Get the Gateway service endpoint
kubectl get svc -n agentgateway-system

# Access your tool via the gateway
curl http://<gateway-endpoint>/mcp
```

## End-to-End (E2E) Testing

### Prerequisites for E2E Tests

- **kind** must be installed and available in PATH
- **Docker** running and accessible
- **kubectl** configured and working

### Running E2E Tests

The E2E tests automatically create an isolated Kind cluster, deploy the operator, run comprehensive tests, and clean up afterwards.

```bash
# Run complete E2E test suite
make test-e2e
```

### Manual E2E Test Setup

If you need to run E2E tests manually or inspect the test environment:

```bash
# Set up test cluster
make setup-test-e2e
```
```bash
# Run E2E tests against the existing cluster
KIND_CLUSTER=tool-gateway-kgateway-test-e2e go test ./test/e2e/ -v -ginkgo.v
```
```bash
# Clean up test cluster when done
make cleanup-test-e2e
```

## Testing Tools and Configuration

The project includes comprehensive test coverage:

- **Unit Tests**: Test suite for the controller
- **E2E Tests**: End-to-end tests in Kind cluster
- **Ginkgo/Gomega**: BDD-style testing framework
- **EnvTest**: Kubernetes API server testing environment

Run tests with:

```bash
# Run unit and integration tests
make test

# Run E2E tests in kind cluster
make test-e2e
```

## Sample Data

The project includes sample manifests to help you get started.

  * **Where to find sample data?**
    Sample manifests are located in the `config/samples/` directory.

  * **How to deploy sample resources?**
    You can deploy sample ToolGateway resources with:

    ```bash
    kubectl apply -k config/samples/
    ```

## Contribution

See [Contribution Guide](https://github.com/agentic-layer/tool-gateway-kgateway?tab=contributing-ov-file) for details on contribution, and the process for submitting pull requests.

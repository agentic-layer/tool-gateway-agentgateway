# Tool Gateway agentgateway Operator

The Tool Gateway agentgateway Operator is a Kubernetes operator that manages `ToolGateway` instances based on [agentgateway](https://agentgateway.dev/). It provides centralized gateway management for tool workloads within the Agentic Layer ecosystem.

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

* **Go**: version 1.26.0 or higher
* **Docker**: version 20.10+ (or a compatible alternative like Podman)
* **kubectl**: The Kubernetes command-line tool
* **kind**: For running Kubernetes locally in Docker
* **helm**: Helm 3+ for installing agentgateway
* **make**: The build automation tool

----

## Getting Started

**Quick Start:**

```shell
# Create local cluster
kind create cluster

# Install required dependencies (cert-manager, Gateway API CRDs, agentgateway, agent-runtime)
make install-deps

# Install the Tool Gateway operator
kubectl apply -f https://github.com/agentic-layer/tool-gateway-agentgateway/releases/latest/download/install.yaml
```

To remove all dependencies from the cluster again:

```shell
kubectl delete -f https://github.com/agentic-layer/tool-gateway-agentgateway/releases/latest/download/install.yaml
make uninstall-deps
```

## How it Works

The Tool Gateway agentgateway Operator creates and manages Gateway API resources based on ToolGateway and ToolServer custom resources:

1. **Gateway Creation**: When a ToolGateway is created, the operator creates a dedicated Gateway with the same name in the same namespace as the ToolGateway with HTTP listener on port 80. Each ToolGateway has its own Gateway instance.

2. **ToolServer Integration**: For each ToolServer resource, the operator creates:
   - **AgentgatewayBackend**: Configures the MCP backend connection to the ToolServer
   - **HTTPRoute**: Routes traffic from the gateway to the AgentgatewayBackend using path-based matching

3. **Automatic Updates**: The operator watches for changes to ToolGateway and ToolServer resources and updates the corresponding Gateway API resources automatically.

### Architecture

```
ToolGateway (CRD)
    |
Gateway (same name and namespace)
    |
HTTPRoute (Gateway API) -> AgentgatewayBackend -> ToolServer
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

After a successful start, you should see the controller manager pod running in the `tool-gateway-agentgateway-system` namespace.

```bash
kubectl get pods -n tool-gateway-agentgateway-system
```

## Configuration

### Prerequisites for ToolGateway

Before creating a ToolGateway, ensure you have:

1. **Gateway API CRDs** installed in your cluster
2. **agentgateway support** installed (see [Getting Started](#getting-started))

### ToolGateway Configuration

To create a agentgateway-based gateway for your tools, define a `ToolGateway` resource:

```yaml
apiVersion: runtime.agentic-layer.ai/v1alpha1
kind: ToolGateway
metadata:
  name: my-tool-gateway
  namespace: my-namespace
spec:
  toolGatewayClassName: agentgateway  # Optional: uses default if not specified
```

This will create a `my-tool-gateway` Gateway in the `my-namespace` namespace.

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

Once deployed, tools are accessible via the ToolGateway's Gateway:

```shell
# Get the Gateway service endpoint (example for 'my-tool-gateway')
kubectl get svc -n my-namespace | grep my-tool-gateway

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

# Install required dependencies
make install-deps

# Run E2E tests against the existing cluster
KIND_CLUSTER=tool-gateway-agentgateway-test-e2e go test ./test/e2e/ -v -ginkgo.v

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

See [Contribution Guide](https://github.com/agentic-layer/tool-gateway-agentgateway?tab=contributing-ov-file) for details on contribution, and the process for submitting pull requests.

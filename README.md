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

The Tool Gateway agentgateway Operator creates and manages Gateway API resources based on ToolGateway, ToolRoute, and ToolServer custom resources:

1. **Gateway Creation**: When a ToolGateway is created, the operator creates a dedicated Gateway with the same name in the same namespace as the ToolGateway with HTTP listener on port 80. Each ToolGateway has its own Gateway instance.

2. **ToolRoute Integration**: For each ToolRoute resource, the operator creates:
   - **AgentgatewayBackend**: Configures the MCP backend connection to the route's upstream (a cluster `ToolServer` service or an external URL).
   - **HTTPRoute**: Routes traffic from the referenced `ToolGateway` to the `AgentgatewayBackend` at the path `/<toolroute-namespace>/<toolroute-name>/mcp`. The reachable URL is surfaced as `ToolRoute.status.url`.
   - **AgentgatewayPolicy** (optional): When `spec.toolFilter` is set, an `AgentgatewayPolicy` is attached to the route's `HTTPRoute` carrying MCP authorization CEL rules translated from the glob allow/deny patterns.

3. **ToolServer**: Describes how an MCP server is deployed. It is consumed by `ToolRoute.spec.upstream.toolServerRef`; exposure through a gateway is always driven by a `ToolRoute`.

4. **Automatic Updates**: The operator watches for changes to ToolGateway and ToolRoute resources and updates the corresponding Gateway API resources automatically.

### Architecture

```
ToolGateway (CRD)
    |
Gateway (same name and namespace)

ToolRoute (CRD)
    |
HTTPRoute (Gateway API) --> AgentgatewayBackend --> upstream (ToolServer or external URL)
    |
AgentgatewayPolicy (optional, when toolFilter is set)
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

#### OpenTelemetry (OTEL) Configuration

The operator automatically translates standard OpenTelemetry environment variables to agentgateway's telemetry configuration. This provides seamless observability integration without requiring manual configuration of agentgateway's config file.

**Supported OTEL Environment Variables:**

- `OTEL_EXPORTER_OTLP_ENDPOINT` - The OTLP exporter endpoint URL
- `OTEL_EXPORTER_OTLP_PROTOCOL` - The protocol to use (grpc or http)
- `OTEL_EXPORTER_OTLP_HEADERS` - Headers to send with telemetry data
- Signal-specific overrides:
  - `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`
  - `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL`
  - `OTEL_EXPORTER_OTLP_TRACES_HEADERS`
  - (Similar for METRICS and LOGS)

**Example:**

```yaml
apiVersion: runtime.agentic-layer.ai/v1alpha1
kind: ToolGateway
metadata:
  name: my-tool-gateway
  namespace: my-namespace
spec:
  toolGatewayClassName: agentgateway
  env:
    # OTEL configuration - automatically translated to agentgateway config
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: "http://otel-collector:4318"
    - name: OTEL_EXPORTER_OTLP_PROTOCOL
      value: "http/protobuf"
    - name: OTEL_EXPORTER_OTLP_HEADERS
      value: "api-key=secret123"
    
    # Other environment variables are passed through as-is
    - name: LOG_LEVEL
      value: "info"
```

The OTEL environment variables are translated to agentgateway's `rawConfig.telemetry` section and are **not** passed as environment variables to the agentgateway container. Other environment variables (like `LOG_LEVEL` above) are passed through unchanged.

See `config/samples/toolgateway_v1alpha1_otel_example.yaml` for complete examples.

### ToolServer Configuration

Define ToolServer resources that represent your MCP server deployments. A `ToolServer`
only describes *what runs*; to actually expose it through a `ToolGateway` you must
create a `ToolRoute`.

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

### ToolRoute Configuration

Create one `ToolRoute` per exposure of a tool server (cluster ToolServer or
external URL) through a `ToolGateway`. Optionally restrict which tools are
visible with `toolFilter`:

```yaml
apiVersion: runtime.agentic-layer.ai/v1alpha1
kind: ToolRoute
metadata:
  name: my-tool-route
  namespace: my-namespace
spec:
  toolGatewayRef:                # Optional; see "Default ToolGateway resolution" below
    name: my-tool-gateway
  upstream:
    # Exactly one of toolServerRef or external must be set
    toolServerRef:
      name: my-tool-server
    # OR: external:
    #   url: https://github-mcp.example.com/mcp
  toolFilter:
    # Glob allow/deny. Deny wins on conflict. If toolFilter is nil, all
    # tools pass through unfiltered.
    allow: ["get_*", "list_*"]
    deny: ["*delete*"]
```

For each ToolRoute, the operator creates:
- An **AgentgatewayBackend** configured with the resolved upstream
- An **HTTPRoute** matching `/<route-namespace>/<route-name>/mcp` on the referenced `ToolGateway`
- An **AgentgatewayPolicy** with MCP `authorization` CEL rules (only when `toolFilter` is set)

The reachable URL is written to `ToolRoute.status.url` and is what consumers read.

#### Default ToolGateway resolution

`spec.toolGatewayRef` is optional. The operator resolves the target `ToolGateway` as follows:

- If `toolGatewayRef` is set, the explicit reference is used. The `namespace` field defaults to the ToolRoute's own namespace when omitted.
- If `toolGatewayRef` is omitted, the operator selects a `ToolGateway` from the well-known `tool-gateway` namespace. If multiple exist there, the first one listed wins (a warning is logged). If none exists, the route's status is set to `NotReady` with reason `ToolRouteGatewayNotFound`; reconciliation retriggers automatically once a default `ToolGateway` is created in that namespace.

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

# Tool Gateway agentgateway Operator

The Tool Gateway agentgateway Operator is a Kubernetes operator that reconciles `ToolGateway` resources based on [agentgateway](https://agentgateway.dev/). For each `ToolRoute` attached to a gateway, it programs the underlying Gateway API resources (`Gateway`, `HTTPRoute`, `AgentgatewayBackend`, optional `AgentgatewayPolicy`) so MCP tool traffic can be routed, filtered, and guarded.

📖 **Documentation:** https://docs.agentic-layer.ai/tool-gateway-agentgateway/

## Development

### Prerequisites

- **Go** 1.26+
- **Docker**
- **kubectl**
- **kind** (used for local development and E2E tests)
- **helm** 3+ (for installing agentgateway)
- **make**

### Build and deploy locally

```shell
# Create a local cluster
kind create cluster

# Install all required dependencies (cert-manager, Gateway API CRDs,
# agentgateway, Agent Runtime Operator)
make install-deps

# Install CRDs into the cluster
make install
# Build docker image
make docker-build
# Load image into kind cluster (not needed if using local registry)
make kind-load
# Deploy the operator to the cluster
make deploy
```

The controller manager runs in the `tool-gateway-agentgateway-system` namespace. To remove all dependencies from the cluster, run `make uninstall-deps`.

### Test

```shell
make lint       # linting
make test       # unit + integration tests
make test-e2e   # E2E tests in a Kind cluster
```

For manual E2E setup against an existing cluster, see the `Makefile` targets `setup-test-e2e` and `cleanup-test-e2e`.

### Verify the local deploy

Apply the bundled `ToolGateway` samples and check the Gateway API resources the operator generates:

```shell
kubectl apply -k config/samples/
kubectl get toolgateways,toolroutes,toolservers
kubectl get gateways,httproutes
```

To exercise the guardrail-adapter flow, apply the guardrails sample instead:

```shell
kubectl apply -k config/samples/guardrails/
```

### Create or Update API and Webhooks

The operator-sdk CLI can be used to create or update APIs and webhooks.
This is the preferred way to add new APIs and webhooks to the operator.
If the operator-sdk CLI is updated, you may need to re-run these commands to update the generated code.

```shell
# Create API for Agent CRD
operator-sdk create api --group runtime --version v1alpha1 --kind Agent

# Create webhook for Agent CRD
operator-sdk create webhook --group runtime --version v1alpha1 --kind Agent --defaulting --programmatic-validation
```

## Contributing

See the [Contribution Guide](https://github.com/agentic-layer/tool-gateway-agentgateway?tab=contributing-ov-file).

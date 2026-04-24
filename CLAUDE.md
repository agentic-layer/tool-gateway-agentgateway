# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Project Overview and Developer Documentation
- @README.md

Reference Documentation
- Overall Agentic Layer Architecture: https://docs.agentic-layer.ai/architecture/main/index.html

### Project Structure

```
├── cmd/main.go           # Operator entry point
├── config/               # Kubernetes manifests and Kustomize configs
│   ├── crd/              # Custom Resource Definitions
│   ├── rbac/             # Role-based access control
│   ├── manager/          # Operator deployment
│   └── samples/          # Example ToolGateway and ToolRoute resources
├── internal/
│   └── controller/       # Reconciliation logic
│       ├── toolgateway_reconciler.go  # Reconciles ToolGateway (Gateway + AgentgatewayParameters + multiplex routes)
│       ├── toolroute_reconciler.go    # Reconciles ToolRoute (per-route Backend + HTTPRoute + optional Policy)
│       ├── toolroute_authz.go         # Translates glob tool-filter to AgentgatewayPolicy CEL rules
│       └── resources.go               # Shared unstructured helpers
└── test/
    ├── e2e/              # End-to-end tests
    └── utils/            # Test utilities
```

### Architecture

- **ToolGatewayReconciler** owns gateway-wide concerns: the `Gateway` resource, `AgentgatewayParameters` (env/envFrom/telemetry translation), and multiplex `HTTPRoute`s (`/mcp` and `/<ns>/mcp`) that aggregate every `ToolServer` exposed to this gateway via a `ToolRoute`.
- **ToolRouteReconciler** owns per-route concerns: for each `ToolRoute` it resolves the upstream (cluster `ToolServer` service or external URL), ensures an `AgentgatewayBackend`, an `HTTPRoute` at `/<route-ns>/<route-name>/mcp`, and — when `spec.toolFilter` is set — an `AgentgatewayPolicy` whose `spec.backend.mcp.authorization.policy.matchExpressions` carries CEL rules translated from glob allow/deny patterns. Populates `ToolRoute.status.url`.
- **Class ownership** is enforced the same way in both reconcilers: the `ToolGatewayClass` of the referenced `ToolGateway` must carry this operator's controller string, or be the default class when none is specified.

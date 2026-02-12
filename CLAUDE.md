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
│   └── samples/          # Example ToolGateway resources
├── internal/
│   └── controller/       # Reconciliation logic
└── test/
    ├── e2e/              # End-to-end tests
    └── utils/            # Test utilities
```

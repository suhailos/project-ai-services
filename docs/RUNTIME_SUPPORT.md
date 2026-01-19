# Runtime Support in AI-Services

AI-Services now supports multiple container runtimes, allowing you to deploy and manage AI applications on both **Podman** (default) and **Kubernetes** platforms.

## Table of Contents

- [Overview](#overview)
- [Supported Runtimes](#supported-runtimes)
- [Configuration](#configuration)
- [Usage](#usage)
- [Runtime-Specific Considerations](#runtime-specific-considerations)
- [Examples](#examples)
- [Troubleshooting](#troubleshooting)

## Overview

The runtime abstraction layer in AI-Services provides a unified interface for managing AI applications across different container orchestration platforms. This allows you to:

- Deploy the same application templates on different runtimes
- Switch between runtimes without changing your workflow
- Leverage platform-specific features when needed

## Supported Runtimes

### 1. Podman (Default)

Podman is a daemonless container engine for developing, managing, and running OCI Containers on your Linux System.

**Features:**
- Rootless containers
- Pod support via Kubernetes YAML
- Compatible with Docker CLI
- No daemon required

**Requirements:**
- Podman installed and running
- Podman socket accessible at `/run/podman/podman.sock`

### 2. Kubernetes

Kubernetes is an open-source container orchestration platform for automating deployment, scaling, and management of containerized applications.

**Features:**
- Production-grade orchestration
- Built-in scaling and self-healing
- Service discovery and load balancing
- Rolling updates and rollbacks

**Requirements:**
- Access to a Kubernetes cluster
- kubectl configured with appropriate credentials
- Kubeconfig file at `~/.kube/config` or specified via `KUBECONFIG` environment variable

## Configuration

### Method 1: Command-Line Flag (Recommended)

Specify the runtime for each command using the `--runtime` flag:

```bash
ai-services --runtime podman application ps
ai-services --runtime kubernetes application ps
```

### Method 2: Environment Variable

Set the `AI_SERVICES_RUNTIME` environment variable:

```bash
export AI_SERVICES_RUNTIME=kubernetes
ai-services application ps
```

### Method 3: Configuration File

Create a configuration file at `~/.ai-services/config.yaml`:

```yaml
runtime:
  type: kubernetes
  kubernetes:
    namespace: default
    kubeconfig: /path/to/kubeconfig  # optional
```

**Priority Order:**
1. Command-line flag (`--runtime`)
2. Environment variable (`AI_SERVICES_RUNTIME`)
3. Configuration file (`~/.ai-services/config.yaml`)
4. Default (podman)

## Usage

### Basic Commands

All ai-services commands work with both runtimes:

```bash
# List applications (Podman - default)
ai-services application ps

# List applications (Kubernetes)
ai-services --runtime kubernetes application ps

# Create application
ai-services --runtime kubernetes application create my-app --template rag

# Delete application
ai-services --runtime kubernetes application delete my-app

# Start/Stop pods
ai-services --runtime kubernetes application start my-app
ai-services --runtime kubernetes application stop my-app

# View logs
ai-services --runtime kubernetes application logs --pod my-pod
```

### Switching Runtimes

You can easily switch between runtimes:

```bash
# Deploy on Podman
ai-services application create my-app --template rag

# Check status on Podman
ai-services application ps my-app

# Switch to Kubernetes
export AI_SERVICES_RUNTIME=kubernetes

# Deploy same app on Kubernetes
ai-services application create my-app --template rag

# Check status on Kubernetes
ai-services application ps my-app
```

## Runtime-Specific Considerations

### Podman

**Advantages:**
- Simpler setup for development
- No cluster required
- Rootless operation
- Direct access to host resources

**Limitations:**
- Single-node only
- Limited scaling capabilities
- Manual high-availability setup

**Best For:**
- Development and testing
- Single-server deployments
- Edge computing scenarios

### Kubernetes

**Advantages:**
- Production-grade orchestration
- Multi-node clustering
- Built-in scaling and load balancing
- Rich ecosystem of tools

**Limitations:**
- More complex setup
- Requires cluster infrastructure
- Some Podman-specific features may not translate directly

**Best For:**
- Production deployments
- Multi-node clusters
- Applications requiring high availability
- Cloud-native environments

## Examples

### Example 1: Deploy RAG Application on Kubernetes

```bash
# Set runtime to Kubernetes
export AI_SERVICES_RUNTIME=kubernetes

# Create RAG application
ai-services application create rag-chatbot --template rag \
  --params ui.port=8080

# Check deployment status
ai-services application ps rag-chatbot

# View logs
ai-services application logs --pod rag-chatbot-ui

# Delete when done
ai-services application delete rag-chatbot
```

### Example 2: Development on Podman, Production on Kubernetes

```bash
# Development on Podman (default)
ai-services application create dev-app --template rag

# Test locally
ai-services application ps dev-app

# Deploy to production Kubernetes cluster
ai-services --runtime kubernetes application create prod-app --template rag \
  --values production-values.yaml

# Monitor production
ai-services --runtime kubernetes application ps prod-app
```

### Example 3: Using Configuration File

Create `~/.ai-services/config.yaml`:

```yaml
runtime:
  type: kubernetes
  kubernetes:
    namespace: ai-services
    kubeconfig: ~/.kube/prod-cluster-config
```

Then use ai-services normally:

```bash
# Will use Kubernetes runtime from config
ai-services application ps

# Override with flag if needed
ai-services --runtime podman application ps
```

## Troubleshooting

### Podman Issues

**Problem:** Cannot connect to Podman socket

```bash
# Check if Podman socket is running
systemctl --user status podman.socket

# Start Podman socket if needed
systemctl --user start podman.socket
```

**Problem:** Permission denied

```bash
# Ensure user has access to Podman
podman ps

# Check socket permissions
ls -l /run/podman/podman.sock
```

### Kubernetes Issues

**Problem:** Cannot connect to Kubernetes cluster

```bash
# Verify kubectl access
kubectl cluster-info

# Check kubeconfig
kubectl config view

# Test connection
kubectl get nodes
```

**Problem:** Namespace not found

```bash
# Create namespace
kubectl create namespace ai-services

# Or update config to use existing namespace
```

**Problem:** Insufficient permissions

```bash
# Check current permissions
kubectl auth can-i create pods

# Ensure service account has required permissions
kubectl describe rolebinding -n ai-services
```

### General Issues

**Problem:** Runtime type not recognized

```bash
# Check valid runtime types
ai-services --help | grep runtime

# Valid values: podman, kubernetes
```

**Problem:** Features not working on Kubernetes

Some Podman-specific features may have limited support on Kubernetes:
- Image listing (handled by kubelet)
- Direct container inspection (use kubectl instead)
- Pod start/stop semantics differ

## Migration Guide

### From Podman to Kubernetes

1. **Export Application Configuration:**
   ```bash
   # Save your values
   ai-services application templates > app-config.yaml
   ```

2. **Prepare Kubernetes Cluster:**
   ```bash
   kubectl create namespace ai-services
   ```

3. **Deploy to Kubernetes:**
   ```bash
   ai-services --runtime kubernetes application create my-app \
     --template rag --values app-config.yaml
   ```

4. **Verify Deployment:**
   ```bash
   ai-services --runtime kubernetes application ps my-app
   kubectl get pods -n ai-services
   ```

### From Kubernetes to Podman

1. **Export Configuration:**
   ```bash
   kubectl get pod my-app -o yaml > pod-config.yaml
   ```

2. **Deploy to Podman:**
   ```bash
   ai-services application create my-app --template rag
   ```

3. **Verify:**
   ```bash
   ai-services application ps my-app
   podman pod ps
   ```

## Best Practices

1. **Use Configuration Files for Production:**
   - Store runtime configuration in version control
   - Use different configs for different environments

2. **Namespace Organization (Kubernetes):**
   - Use separate namespaces for different environments
   - Apply resource quotas and limits

3. **Testing:**
   - Test on Podman first for rapid iteration
   - Deploy to Kubernetes for integration testing

4. **Monitoring:**
   - Use `ai-services application ps` for quick status checks
   - Leverage native tools (kubectl, podman) for detailed inspection

5. **Resource Management:**
   - Set appropriate resource limits in templates
   - Monitor resource usage across runtimes

## Additional Resources

- [Podman Documentation](https://docs.podman.io/)
- [Kubernetes Documentation](https://kubernetes.io/docs/)
- [AI-Services GitHub Repository](https://github.com/IBM/project-ai-services)
- [AI-Services CLI Reference](https://www.ibm.com/docs/aiservices)

## Support

For issues or questions:
- Open an issue on [GitHub](https://github.com/IBM/project-ai-services/issues)
- Check existing documentation
- Review troubleshooting section above
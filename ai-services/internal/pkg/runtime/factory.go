package runtime

import (
	"fmt"
	"os"
	"strings"

	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/kubernetes"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/podman"
)

const (
	// EnvRuntimeType is the environment variable for runtime type
	EnvRuntimeType = "AI_SERVICES_RUNTIME"
)

// Factory creates runtime instances based on configuration
type Factory struct {
	runtimeType RuntimeType
}

// NewFactory creates a new runtime factory with the specified runtime type
func NewFactory(runtimeType RuntimeType) *Factory {
	return &Factory{
		runtimeType: runtimeType,
	}
}

// NewFactoryFromEnv creates a factory using environment variable or default
func NewFactoryFromEnv() *Factory {
	runtimeType := RuntimeTypePodman // default
	if envRuntime := os.Getenv(EnvRuntimeType); envRuntime != "" {
		rt := RuntimeType(strings.ToLower(envRuntime))
		if rt.Valid() {
			runtimeType = rt
		} else {
			logger.Warningf("Invalid runtime type in %s: %s, using default: %s\n", 
				EnvRuntimeType, envRuntime, RuntimeTypePodman)
		}
	}
	return NewFactory(runtimeType)
}

// Create creates a runtime instance based on the factory configuration
func (f *Factory) Create() (Runtime, error) {
	return CreateRuntime(f.runtimeType)
}

// GetRuntimeType returns the configured runtime type
func (f *Factory) GetRuntimeType() RuntimeType {
	return f.runtimeType
}

// CreateRuntime creates a runtime instance based on the specified type
func CreateRuntime(runtimeType RuntimeType) (Runtime, error) {
	switch runtimeType {
	case RuntimeTypePodman:
		logger.Infof("Initializing Podman runtime\n", logger.VerbosityLevelDebug)
		client, err := podman.NewPodmanClient()
		if err != nil {
			return nil, fmt.Errorf("failed to create Podman client: %w", err)
		}
		return client, nil

	case RuntimeTypeKubernetes:
		logger.Infof("Initializing Kubernetes runtime\n", logger.VerbosityLevelDebug)
		client, err := kubernetes.NewKubernetesClient()
		if err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
		}
		return client, nil

	default:
		return nil, fmt.Errorf("unsupported runtime type: %s", runtimeType)
	}
}

// CreateRuntimeWithNamespace creates a runtime with a specific namespace (for Kubernetes)
func CreateRuntimeWithNamespace(runtimeType RuntimeType, namespace string) (Runtime, error) {
	switch runtimeType {
	case RuntimeTypePodman:
		// Podman doesn't use namespaces in the same way
		return CreateRuntime(runtimeType)

	case RuntimeTypeKubernetes:
		logger.Infof("Initializing Kubernetes runtime with namespace: %s\n", namespace, logger.VerbosityLevelDebug)
		client, err := kubernetes.NewKubernetesClientWithNamespace(namespace)
		if err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
		}
		return client, nil

	default:
		return nil, fmt.Errorf("unsupported runtime type: %s", runtimeType)
	}
}

// Made with Bob

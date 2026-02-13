package application

import (
	"fmt"
	"os"

	"github.com/project-ai-services/ai-services/internal/pkg/runtime"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
)

// Factory creates Application instances based on runtime type
type Factory struct {
	runtimeType types.RuntimeType
}

// NewFactory creates a new Application factory with the specified runtime type
func NewFactory(runtimeType types.RuntimeType) *Factory {
	return &Factory{
		runtimeType: runtimeType,
	}
}

// NewFactoryFromEnv creates a new Application factory using the AI_SERVICES_RUNTIME environment variable
func NewFactoryFromEnv() *Factory {
	runtimeType := types.RuntimeType(os.Getenv("AI_SERVICES_RUNTIME"))
	if runtimeType == "" {
		runtimeType = types.RuntimeTypePodman // default to Podman
	}
	return NewFactory(runtimeType)
}

// Create creates an Application instance based on the factory's runtime type
func (f *Factory) Create() (Application, error) {
	// Create the runtime client first
	runtimeFactory := runtime.NewRuntimeFactory(f.runtimeType)
	runtimeClient, err := runtimeFactory.Create()
	if err != nil {
		return nil, fmt.Errorf("failed to create runtime client: %w", err)
	}

	switch f.runtimeType {
	case types.RuntimeTypePodman:
		return NewPodmanApplication(runtimeClient), nil
	case types.RuntimeTypeOpenShift:
		return NewOpenshiftApplication(runtimeClient), nil
	default:
		return nil, fmt.Errorf("unsupported runtime type: %s", f.runtimeType)
	}
}

// Made with Bob

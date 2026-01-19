package runtime

import (
	"io"

	"github.com/containers/podman/v5/libpod/define"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/domain/entities/types"
)

// Runtime defines the interface for container runtime operations
// This interface abstracts the underlying runtime (Podman, Kubernetes, etc.)
type Runtime interface {
	// Image operations
	ListImages() ([]*types.ImageSummary, error)
	PullImage(image string, options *images.PullOptions) error

	// Pod operations
	ListPods(filters map[string][]string) ([]Pod, error)
	CreatePod(body io.Reader) (*types.KubePlayReport, error)
	DeletePod(id string, force *bool) error
	InspectPod(nameOrID string) (*types.PodInspectReport, error)
	PodExists(nameOrID string) (bool, error)
	StopPod(id string) error
	StartPod(id string) error
	PodLogs(podNameOrID string) error

	// Container operations
	ListContainers(filters map[string][]string) (any, error)
	InspectContainer(nameOrId string) (*define.InspectContainerData, error)
	ContainerExists(nameOrID string) (bool, error)
	ContainerLogs(containerNameOrID string) error

	// Runtime type identification
	Type() RuntimeType
}

// RuntimeType represents the type of container runtime
type RuntimeType string

const (
	RuntimeTypePodman     RuntimeType = "podman"
	RuntimeTypeKubernetes RuntimeType = "kubernetes"
)

// String returns the string representation of RuntimeType
func (r RuntimeType) String() string {
	return string(r)
}

// Valid checks if the runtime type is valid
func (r RuntimeType) Valid() bool {
	switch r {
	case RuntimeTypePodman, RuntimeTypeKubernetes:
		return true
	default:
		return false
	}
}

// Made with Bob

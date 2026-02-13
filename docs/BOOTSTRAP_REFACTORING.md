# Bootstrap Refactoring Documentation

## Overview

This document describes the refactoring of the bootstrap functionality to follow the dependency inversion principle, similar to the runtime interface pattern used in the project.

## Architecture

### Before Refactoring

Previously, all bootstrap logic was contained in `cmd/ai-services/cmd/bootstrap/` with:
- `bootstrap.go` - Main command
- `configure.go` - Configuration logic (Podman-specific)
- `validate.go` - Validation logic

All logic was tightly coupled to Podman runtime, making it difficult to support other runtimes like OpenShift.

### After Refactoring

The bootstrap logic has been moved to `internal/pkg/bootstrap/` with a clean interface-based design:

```
internal/pkg/bootstrap/
├── interface.go      # Bootstrap interface definition
├── bootstrap.go      # Factory and creation logic
├── podman.go         # Podman-specific implementation
└── openshift.go      # OpenShift-specific implementation
```

## Interface Design

### Bootstrap Interface

```go
type Bootstrap interface {
    // Configure performs the complete configuration of the environment
    Configure() error
    
    // Validate runs all validation checks
    Validate(skip map[string]bool) error
    
    // InstallRuntime installs the container runtime
    InstallRuntime() error
    
    // ConfigureRuntime configures the container runtime
    ConfigureRuntime() error
    
    // ConfigureHardware configures hardware-specific settings
    ConfigureHardware() error
    
    // Type returns the runtime type
    Type() types.RuntimeType
}
```

## Implementations

### Podman Bootstrap

The `PodmanBootstrap` implementation handles:
- Installing Podman via DNF
- Configuring Podman socket and services
- Setting up Spyre cards and VFIO kernel modules
- Running servicereport tool
- Configuring user groups and udev rules
- Full validation suite

### OpenShift Bootstrap

The `OpenshiftBootstrap` implementation:
- Assumes cluster is already set up
- Skips hardware-specific validations (Spyre, NUMA, servicereport)
- Validates cluster connectivity
- Runs platform-level validations only

## Factory Pattern

### BootstrapFactory

Similar to `RuntimeFactory`, the `BootstrapFactory` creates bootstrap instances:

```go
// Create from environment variable
factory := bootstrap.NewFactoryFromEnv()
bootstrapInstance, err := factory.Create()

// Or specify runtime type explicitly
factory := bootstrap.NewBootstrapFactory(types.RuntimeTypePodman)
bootstrapInstance, err := factory.Create()
```

### Environment Variable

The bootstrap runtime is controlled by the `AI_SERVICES_RUNTIME` environment variable:
- `podman` (default) - Uses PodmanBootstrap
- `openshift` - Uses OpenshiftBootstrap

## Command Updates

### Main Bootstrap Command

```go
// cmd/ai-services/cmd/bootstrap/bootstrap.go
factory := bootstrap.NewFactoryFromEnv()
bootstrapInstance, err := factory.Create()

if err := bootstrapInstance.Configure(); err != nil {
    return err
}

if err := bootstrapInstance.Validate(nil); err != nil {
    return err
}
```

### Configure Subcommand

```go
// cmd/ai-services/cmd/bootstrap/configure.go
factory := bootstrap.NewFactoryFromEnv()
bootstrapInstance, err := factory.Create()

if err := bootstrapInstance.Configure(); err != nil {
    return err
}
```

### Validate Subcommand

```go
// cmd/ai-services/cmd/bootstrap/validate.go
factory := bootstrap.NewFactoryFromEnv()
bootstrapInstance, err := factory.Create()

skip := helpers.ParseSkipChecks(skipChecks)
if err := bootstrapInstance.Validate(skip); err != nil {
    return err
}
```

## Benefits

1. **Separation of Concerns**: Business logic is separated from CLI commands
2. **Runtime Flexibility**: Easy to add support for new runtimes
3. **Testability**: Interface-based design enables better unit testing
4. **Consistency**: Follows the same pattern as the runtime interface
5. **Maintainability**: Changes to bootstrap logic don't affect command structure

## Usage Examples

### Default (Podman) Bootstrap

```bash
# Uses Podman bootstrap by default
ai-services bootstrap

# Or explicitly set
export AI_SERVICES_RUNTIME=podman
ai-services bootstrap
```

### OpenShift Bootstrap

```bash
# Use OpenShift bootstrap
export AI_SERVICES_RUNTIME=openshift
ai-services bootstrap

# Validate only
ai-services bootstrap validate
```

### Skip Validations

```bash
# Skip specific checks (works with both runtimes)
ai-services bootstrap validate --skip-validation rhn,power
```

## Migration Notes

### For Developers

1. All bootstrap business logic is now in `internal/pkg/bootstrap/`
2. Command files in `cmd/ai-services/cmd/bootstrap/` are now thin wrappers
3. To add a new runtime:
   - Create a new file in `internal/pkg/bootstrap/` (e.g., `kubernetes.go`)
   - Implement the `Bootstrap` interface
   - Add the runtime type to `types.RuntimeType`
   - Update the factory in `bootstrap.go`

### For Users

No changes to command-line interface or usage. The refactoring is transparent to end users.

## Testing

To test the implementation:

```bash
# Build the project
cd ai-services
go build -o ai-services ./cmd/ai-services

# Test Podman bootstrap
./ai-services bootstrap validate

# Test OpenShift bootstrap
export AI_SERVICES_RUNTIME=openshift
./ai-services bootstrap validate
```

## Future Enhancements

1. Add unit tests for each bootstrap implementation
2. Add integration tests for the factory pattern
3. Implement Kubernetes bootstrap (if needed)
4. Add more granular hardware configuration methods
5. Support custom bootstrap implementations via plugins

## Related Files

- `internal/pkg/runtime/interface.go` - Runtime interface (similar pattern)
- `internal/pkg/runtime/runtime.go` - Runtime factory (similar pattern)
- `internal/pkg/validators/validators.go` - Validation registry
- `docs/RUNTIME_SUPPORT.md` - Runtime support documentation
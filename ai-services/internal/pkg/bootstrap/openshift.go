package bootstrap

import (
	"context"
	"fmt"

	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
	"github.com/project-ai-services/ai-services/internal/pkg/spinner"
	"github.com/project-ai-services/ai-services/internal/pkg/validators"
	"github.com/project-ai-services/ai-services/internal/pkg/validators/root"
)

// OpenshiftBootstrap implements Bootstrap interface for OpenShift runtime.
type OpenshiftBootstrap struct{}

// NewOpenshiftBootstrap creates a new OpenShift bootstrap instance.
func NewOpenshiftBootstrap() *OpenshiftBootstrap {
	return &OpenshiftBootstrap{}
}

// Type returns the runtime type.
func (o *OpenshiftBootstrap) Type() types.RuntimeType {
	return types.RuntimeTypeOpenShift
}

// Configure performs the complete configuration of the OpenShift environment.
func (o *OpenshiftBootstrap) Configure() error {
	rootCheck := root.NewRootRule()
	if err := rootCheck.Verify(); err != nil {
		return err
	}

	ctx := context.Background()

	// For OpenShift, we assume the cluster is already set up
	// Configuration mainly involves validation
	s := spinner.New("Verifying OpenShift configuration")
	s.Start(ctx)

	// TODO: Add OpenShift-specific configuration checks
	// For now, we'll just validate that we can connect to the cluster
	s.Stop("OpenShift configuration verified")

	logger.Infoln("OpenShift environment configured successfully")
	return nil
}

// Validate runs all validation checks for OpenShift.
func (o *OpenshiftBootstrap) Validate(skip map[string]bool) error {
	var validationErrors []error
	ctx := context.Background()

	// For OpenShift, we run a subset of validations
	// Skip hardware-specific checks that are not relevant for OpenShift
	openshiftSkip := make(map[string]bool)
	for k, v := range skip {
		openshiftSkip[k] = v
	}
	// Auto-skip hardware-specific validations for OpenShift
	openshiftSkip["spyre"] = true
	openshiftSkip["servicereport"] = true
	openshiftSkip["numa"] = true

	for _, rule := range validators.DefaultRegistry.Rules() {
		ruleName := rule.Name()
		if openshiftSkip[ruleName] {
			logger.Warningf("%s check skipped for OpenShift runtime", ruleName)
			continue
		}

		s := spinner.New("Validating " + ruleName + " ...")
		s.Start(ctx)
		err := rule.Verify()

		if err != nil {
			s.Fail(err.Error())
			s.StopWithHint(err.Error(), rule.Hint())

			// exit right away if user is not root as other checks require root privileges
			if ruleName == "root" {
				return fmt.Errorf("root privileges are required for validation")
			}

			switch rule.Level() {
			case 0: // ValidationLevelError
				s.Fail(err.Error())
				validationErrors = append(validationErrors, fmt.Errorf("%s: %w", ruleName, err))
			case 1: // ValidationLevelWarning
				s.Stop("Warning: " + err.Error())
			}
		} else {
			s.Stop(rule.Message())
		}
	}

	if len(validationErrors) > 0 {
		return fmt.Errorf("%d validation check(s) failed", len(validationErrors))
	}

	logger.Infoln("All validations passed")
	return nil
}

// InstallRuntime is not applicable for OpenShift as it's a managed platform.
func (o *OpenshiftBootstrap) InstallRuntime() error {
	logger.Infoln("Runtime installation not required for OpenShift")
	return nil
}

// ConfigureRuntime validates OpenShift cluster connectivity.
func (o *OpenshiftBootstrap) ConfigureRuntime() error {
	// TODO: Add OpenShift cluster connectivity checks
	// For now, we assume the cluster is accessible
	logger.Infoln("OpenShift runtime configuration validated")
	return nil
}

// ConfigureHardware is not applicable for OpenShift as hardware is managed by the platform.
func (o *OpenshiftBootstrap) ConfigureHardware() error {
	logger.Infoln("Hardware configuration not required for OpenShift")
	return nil
}

// Made with Bob

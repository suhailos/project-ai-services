package bootstrap

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/project-ai-services/ai-services/internal/pkg/cli/helpers"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
	"github.com/project-ai-services/ai-services/internal/pkg/spinner"
	"github.com/project-ai-services/ai-services/internal/pkg/validators"
	"github.com/project-ai-services/ai-services/internal/pkg/validators/root"
	"github.com/project-ai-services/ai-services/internal/pkg/validators/spyre"
)

const (
	podmanSocketWaitDuration = 2 * time.Second
	contextTimeout           = 30 * time.Second
)

// PodmanBootstrap implements Bootstrap interface for Podman runtime.
type PodmanBootstrap struct{}

// NewPodmanBootstrap creates a new Podman bootstrap instance.
func NewPodmanBootstrap() *PodmanBootstrap {
	return &PodmanBootstrap{}
}

// Type returns the runtime type.
func (p *PodmanBootstrap) Type() types.RuntimeType {
	return types.RuntimeTypePodman
}

// Configure performs the complete configuration of the Podman environment.
func (p *PodmanBootstrap) Configure() error {
	rootCheck := root.NewRootRule()
	if err := rootCheck.Verify(); err != nil {
		return err
	}

	ctx := context.Background()

	// Install Podman if needed
	if err := p.InstallRuntime(); err != nil {
		return err
	}

	// Configure Podman
	s := spinner.New("Verifying podman configuration")
	s.Start(ctx)
	if err := validators.PodmanHealthCheck(); err != nil {
		s.UpdateMessage("Configuring podman")
		if err := p.ConfigureRuntime(); err != nil {
			s.Fail("failed to configure podman")
			return err
		}
		s.Stop("podman configured successfully")
	} else {
		s.Stop("Podman already configured")
	}

	// Configure Hardware (Spyre cards)
	if err := p.ConfigureHardware(); err != nil {
		return err
	}

	logger.Infoln("LPAR configured successfully")
	return nil
}

// Validate runs all validation checks.
func (p *PodmanBootstrap) Validate(skip map[string]bool) error {
	var validationErrors []error
	ctx := context.Background()

	for _, rule := range validators.DefaultRegistry.Rules() {
		ruleName := rule.Name()
		if skip[ruleName] {
			logger.Warningf("%s check skipped; Proceeding without validation may result in deployment failure.", ruleName)
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

// InstallRuntime installs Podman if not already installed.
func (p *PodmanBootstrap) InstallRuntime() error {
	ctx := context.Background()
	s := spinner.New("Checking podman installation")
	s.Start(ctx)

	if _, err := validators.Podman(); err != nil {
		s.UpdateMessage("Installing podman")
		if err := installPodman(); err != nil {
			s.Fail("failed to install podman")
			return err
		}
		s.Stop("podman installed successfully")
	} else {
		s.Stop("podman already installed")
	}

	return nil
}

// ConfigureRuntime configures Podman runtime.
func (p *PodmanBootstrap) ConfigureRuntime() error {
	return setupPodman()
}

// ConfigureHardware configures hardware-specific settings (Spyre cards).
func (p *PodmanBootstrap) ConfigureHardware() error {
	ctx := context.Background()
	s := spinner.New("Checking spyre card configuration")
	s.Start(ctx)

	if err := runServiceReport(); err != nil {
		s.Fail("failed to configure spyre card")
		return err
	}

	s.Stop("Spyre cards configuration validated successfully.")
	return nil
}

// Helper functions

func installPodman() error {
	cmd := exec.Command("dnf", "-y", "install", "podman")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install podman: %v, output: %s", err, string(out))
	}
	return nil
}

func setupPodman() error {
	// start podman socket
	if err := systemctl("start", "podman.socket"); err != nil {
		return fmt.Errorf("failed to start podman socket: %w", err)
	}
	// enable podman socket
	if err := systemctl("enable", "podman.socket"); err != nil {
		return fmt.Errorf("failed to enable podman socket: %w", err)
	}

	logger.Infoln("Waiting for podman socket to be ready...", logger.VerbosityLevelDebug)
	time.Sleep(podmanSocketWaitDuration) // wait for socket to be ready

	if err := validators.PodmanHealthCheck(); err != nil {
		return fmt.Errorf("podman health check failed after configuration: %w", err)
	}

	logger.Infof("Podman configured successfully.")
	return nil
}

func systemctl(action, unit string) error {
	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "systemctl", action, unit)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to %s %s: %v, output: %s", action, unit, err, string(out))
	}
	return nil
}

func runServiceReport() error {
	// validate spyre attachment first before running servicereport
	spyreCheck := spyre.NewSpyreRule()
	err := spyreCheck.Verify()
	if err != nil {
		return err
	}

	// Create host directories for vfio
	cmd := `mkdir -p /etc/modules-load.d; mkdir -p /etc/udev/rules.d/`
	_, err = exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		return fmt.Errorf("❌ failed to create host volume mounts for servicereport tool %w", err)
	}

	// load vfio kernel modules
	cmd = `modprobe vfio_pci`
	_, err = exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		return fmt.Errorf("❌ failed to load vfio kernel modules for spyre %w", err)
	}
	logger.Infoln("VFIO kernel modules loaded on the host", logger.VerbosityLevelDebug)

	if err := helpers.RunServiceReportContainer("servicereport -r -p spyre", "configure"); err != nil {
		return err
	}

	if err := configureUsergroup(); err != nil {
		return err
	}

	if err := reloadUdevRules(); err != nil {
		return err
	}

	cards, err := helpers.ListSpyreCards()
	if err != nil || len(cards) == 0 {
		return fmt.Errorf("❌ failed to list spyre cards on LPAR %w", err)
	}
	num_spyre_cards := len(cards)

	// check if kernel modules for vfio are loaded
	if err := checkKernelModulesLoaded(num_spyre_cards); err != nil {
		return err
	}

	return nil
}

func configureUsergroup() error {
	cmd_str := `groupadd sentient; usermod -aG sentient $USER`
	cmd := exec.Command("bash", "-c", cmd_str)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create sentient group and add current user to the sentient group. Error: %w, output: %s", err, string(out))
	}
	return nil
}

func reloadUdevRules() error {
	cmd := `udevadm control --reload-rules`
	_, err := exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		return fmt.Errorf("failed to reload udev rules. Error: %w", err)
	}
	return nil
}

func checkKernelModulesLoaded(num_spyre_cards int) error {
	vfio_cmd := `lspci -k -d 1014:06a7 | grep "Kernel driver in use: vfio-pci" | wc -l`
	out, err := exec.Command("bash", "-c", vfio_cmd).Output()
	if err != nil {
		return fmt.Errorf("❌ failed to check vfio cards with kernel modules loaded %w", err)
	}

	num_vf_cards, err := strconv.Atoi(strings.TrimSuffix(string(out), "\n"))
	if err != nil {
		return fmt.Errorf("❌ failed to convert number of virtual spyre cards count from string to integer %w", err)
	}

	if num_vf_cards != num_spyre_cards {
		logger.Infof("failed to detect vfio cards, reloading vfio kernel modules..")
		// reload vfio kernel modules
		cmd := `rmmod vfio_pci; modprobe vfio_pci`
		_, err = exec.Command("bash", "-c", cmd).Output()
		if err != nil {
			return fmt.Errorf("❌ failed to reload vfio kernel modules for spyre %w", err)
		}
		logger.Infoln("VFIO kernel modules reloaded on the host", logger.VerbosityLevelDebug)
	}

	return nil
}

// Made with Bob

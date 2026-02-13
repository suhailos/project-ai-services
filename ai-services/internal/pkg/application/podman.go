package application

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/project-ai-services/ai-services/internal/pkg/bootstrap"
	"github.com/project-ai-services/ai-services/internal/pkg/cli/helpers"
	"github.com/project-ai-services/ai-services/internal/pkg/cli/templates"
	"github.com/project-ai-services/ai-services/internal/pkg/constants"
	"github.com/project-ai-services/ai-services/internal/pkg/image"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	"github.com/project-ai-services/ai-services/internal/pkg/models"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
	"github.com/project-ai-services/ai-services/internal/pkg/specs"
	"github.com/project-ai-services/ai-services/internal/pkg/spinner"
	"github.com/project-ai-services/ai-services/internal/pkg/utils"
	"github.com/project-ai-services/ai-services/internal/pkg/validators"
	"github.com/project-ai-services/ai-services/internal/pkg/vars"
)

var (
	extraContainerReadinessTimeout = 5 * time.Minute
	containerCreationTimeout       = 10 * time.Minute
	envMutex                       sync.Mutex
)

// PodmanApplication implements the Application interface for Podman runtime
type PodmanApplication struct {
	runtime      runtime.Runtime
	isOutputWide bool
}

// NewPodmanApplication creates a new PodmanApplication instance
func NewPodmanApplication(runtimeClient runtime.Runtime) *PodmanApplication {
	return &PodmanApplication{
		runtime: runtimeClient,
	}
}

// Create deploys a new application based on a template
func (p *PodmanApplication) Create(ctx context.Context, opts CreateOptions) error {
	// Validate the LPAR before creating the application
	logger.Infof("Validating the LPAR environment before creating application '%s'...\n", opts.Name)

	// Create bootstrap instance and validate
	factory := bootstrap.NewFactoryFromEnv()
	bootstrapInstance, err := factory.Create()
	if err != nil {
		return fmt.Errorf("failed to create bootstrap instance: %w", err)
	}

	skip := helpers.ParseSkipChecks(opts.SkipChecks)
	if err := bootstrapInstance.Validate(skip); err != nil {
		return fmt.Errorf("bootstrap validation failed: %w", err)
	}

	// Proceed to create application
	logger.Infof("Creating application '%s' using template '%s'\n", opts.Name, opts.TemplateName)

	// set SMT level to target value
	s := spinner.New("Checking SMT level")
	s.Start(ctx)
	err = p.setSMTLevel(opts.TemplateName)
	if err != nil {
		s.Fail("failed to set SMT level")
		return fmt.Errorf("failed to set SMT level: %w", err)
	}
	s.Stop("SMT level configured successfully")

	tp := templates.NewEmbedTemplateProvider(templates.EmbedOptions{})

	// validate whether the provided template name is correct
	if err := validators.ValidateAppTemplateExist(tp, opts.TemplateName); err != nil {
		return err
	}

	tmpls, err := tp.LoadAllTemplates(opts.TemplateName)
	if err != nil {
		return fmt.Errorf("failed to parse the templates: %w", err)
	}

	// load metadata.yml to read the app metadata
	appMetadata, err := tp.LoadMetadata(opts.TemplateName, true)
	if err != nil {
		return fmt.Errorf("failed to read the app metadata: %w", err)
	}

	if err := p.verifyPodTemplateExists(tmpls, appMetadata); err != nil {
		return fmt.Errorf("failed to verify pod template: %w", err)
	}

	// Check if pods already exists with the given application name
	existingPods, err := helpers.CheckExistingPodsForApplication(p.runtime, opts.Name)
	if err != nil {
		return fmt.Errorf("failed while checking existing pods for application: %w", err)
	}

	// if all the pods for given application are already deployed, just log and do not proceed further
	if len(existingPods) == len(tmpls) {
		logger.Infof("Pods for given app: %s are already deployed. Please use 'ai-services application ps %s' to see the pods deployed\n", opts.Name, opts.Name)
		return nil
	}

	// ---- Validate Spyre card Requirements ----
	reqSpyreCardsCount, err := p.calculateReqSpyreCards(tp, utils.ExtractMapKeys(tmpls), opts.TemplateName, opts.Name)
	if err != nil {
		return fmt.Errorf("failed to calculateReqSpyreCards: %w", err)
	}

	var pciAddresses []string
	if reqSpyreCardsCount > 0 {
		// calculate the actual available spyre cards
		pciAddresses, err = helpers.FindFreeSpyreCards()
		if err != nil {
			return fmt.Errorf("failed to find free Spyre Cards: %w", err)
		}
		actualSpyreCardsCount := len(pciAddresses)

		// validate spyre card requirements
		if err := p.validateSpyreCardRequirements(reqSpyreCardsCount, actualSpyreCardsCount); err != nil {
			return err
		}
	}

	// ---- Download Container Images ----
	if err := p.downloadImagesForTemplate(opts.TemplateName, opts.Name, opts.ImagePullPolicy); err != nil {
		return err
	}

	// Download models if flag is set to true(default: true)
	if !opts.SkipModelDownload {
		s = spinner.New("Downloading models as part of application creation...")
		s.Start(ctx)
		models, err := helpers.ListModels(opts.TemplateName, opts.Name)
		if err != nil {
			s.Fail("failed to list models")
			return err
		}
		logger.Infoln("Downloading models required for application template " + opts.TemplateName + ":")
		for _, model := range models {
			s.UpdateMessage("Downloading model: " + model + "...")
			err = utils.Retry(vars.RetryCount, vars.RetryInterval, nil, func() error {
				return helpers.DownloadModel(model, vars.ModelDirectory)
			})
			if err != nil {
				s.Fail("failed to download model: " + model)
				return fmt.Errorf("failed to download model: %w", err)
			}
		}
		s.Stop("Model download completed.")
	}

	// Loop through all pod templates, render and run kube play
	logger.Infof("Total Pod Templates to be processed: %d\n", len(tmpls))

	s = spinner.New("Deploying application '" + opts.Name + "'...")
	s.Start(ctx)
	// execute the pod Templates
	if err := p.executePodTemplates(tp, opts.Name, appMetadata, tmpls, pciAddresses, existingPods, opts.ValuesFiles, opts.ArgParams); err != nil {
		return err
	}
	s.Stop("Application '" + opts.Name + "' deployed successfully")

	logger.Infoln("-------")

	// print the next steps to be performed at the end of create
	if err := helpers.PrintNextSteps(p.runtime, opts.Name, opts.TemplateName); err != nil {
		// do not want to fail the overall create if we cannot print next steps
		logger.Infof("failed to display next steps: %v\n", err)
		return nil
	}

	return nil
}

// Delete removes an application and its associated resources
func (p *PodmanApplication) Delete(opts DeleteOptions) error {
	appDir := filepath.Join(constants.ApplicationsPath, filepath.Base(opts.Name))
	appExists := dirExists(appDir)

	pods, err := p.runtime.ListPods(map[string][]string{
		"label": {fmt.Sprintf("ai-services.io/application=%s", opts.Name)},
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}
	podsExists := len(pods) != 0

	if !podsExists {
		logger.Infof("No pods found for application: %s\n", opts.Name)
		return nil
	}

	// print relevant app pod status
	p.logPodsToBeDeleted(opts.Name, pods)

	if !opts.AutoYes {
		confirmDelete, err := p.deleteConfirmation(opts.Name, podsExists, appExists, opts.SkipCleanup)
		if err != nil {
			return err
		}
		if !confirmDelete {
			logger.Infoln("Deletion cancelled")
			return nil
		}
	}

	logger.Infoln("Proceeding with deletion...")

	if err := p.podsDeletion(pods); err != nil {
		return err
	}

	if appExists && !opts.SkipCleanup {
		if err := p.appDataDeletion(appDir); err != nil {
			return err
		}
	}

	return nil
}

func (p *PodmanApplication) logPodsToBeDeleted(appName string, pods []types.Pod) {
	logger.Infof("Found %d pods for given applicationName: %s.\n", len(pods), appName)
	logger.Infoln("Below are the list of pods to be deleted")
	for _, pod := range pods {
		logger.Infof("\t-> %s\n", pod.Name)
	}
}

func (p *PodmanApplication) deleteConfirmation(appName string, podsExists, appExists, skipCleanup bool) (bool, error) {
	var confirmActionPrompt string
	if podsExists && appExists && !skipCleanup {
		confirmActionPrompt = "Are you sure you want to delete the above pods and application data? "
	} else if podsExists {
		confirmActionPrompt = "Are you sure you want to delete the above pods? "
	} else if appExists && !skipCleanup {
		confirmActionPrompt = "Are you sure you want to delete the application data? "
	} else {
		logger.Infof("Application %s does not exist", appName)
		return false, nil
	}

	confirmDelete, err := utils.ConfirmAction(confirmActionPrompt)
	if err != nil {
		return confirmDelete, fmt.Errorf("failed to take user input: %w", err)
	}

	return confirmDelete, nil
}

func (p *PodmanApplication) podsDeletion(pods []types.Pod) error {
	var errors []string

	for _, pod := range pods {
		logger.Infof("Deleting pod: %s\n", pod.Name)

		if err := p.runtime.DeletePod(pod.ID, utils.BoolPtr(true)); err != nil {
			errors = append(errors, fmt.Sprintf("pod %s: %v", pod.Name, err))
			continue
		}

		logger.Infof("Successfully removed pod: %s\n", pod.Name)
	}

	// Aggregate errors at the end
	if len(errors) > 0 {
		return fmt.Errorf("failed to remove pods: \n%s", strings.Join(errors, "\n"))
	}

	return nil
}

func (p *PodmanApplication) appDataDeletion(appDir string) error {
	logger.Infoln("Cleaning up application data")

	if err := os.RemoveAll(appDir); err != nil {
		return fmt.Errorf("failed to delete application data: %w", err)
	}

	logger.Infoln("Application data cleaned up successfully")

	return nil
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Start starts a stopped application
func (p *PodmanApplication) Start(opts StartOptions) error {
	pods, err := p.fetchPodsFromRuntime(opts.Name)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		logger.Infof("No pods found with given application: %s\n", opts.Name)
		return nil
	}

	// Filter pods based on provided pod names or annotation
	podsToStart, err := p.fetchPodsToStart(pods, opts.PodNames)
	if err != nil {
		return err
	}
	if len(podsToStart) == 0 {
		logger.Infof("Invalid/No pods found to start for given application: %s\n", opts.Name)
		return nil
	}

	return p.confirmAndStartPods(podsToStart, opts.AutoYes, opts.SkipLogs)
}

// Start implementation helper methods
func (p *PodmanApplication) fetchPodsFromRuntime(appName string) ([]types.Pod, error) {
	pods, err := p.runtime.ListPods(map[string][]string{
		"label": {fmt.Sprintf("ai-services.io/application=%s", appName)},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}
	return pods, nil
}

func (p *PodmanApplication) fetchPodsToStart(pods []types.Pod, podNames []string) ([]types.Pod, error) {
	if len(podNames) > 0 {
		return p.filterPodsByNameForStart(pods, podNames)
	}
	// No pod names provided, start pods based on annotation
	return p.filterPodsByAnnotationForStart(pods)
}

func (p *PodmanApplication) confirmAndStartPods(podsToStart []types.Pod, autoYes, skipLogs bool) error {
	p.logPodsToStart(podsToStart)
	printLogs := p.shouldPrintLogs(podsToStart, skipLogs)

	if !autoYes {
		confirmStart, err := utils.ConfirmAction("Are you sure you want to start above pods? ")
		if err != nil {
			return fmt.Errorf("failed to take user input: %w", err)
		}
		if !confirmStart {
			logger.Infoln("Skipping starting of pods")
			return nil
		}
	}

	logger.Infoln("Proceeding to start pods...")

	if err := p.startPods(podsToStart); err != nil {
		return err
	}

	if printLogs {
		if err := p.printPodLogs(podsToStart); err != nil {
			return err
		}
	}

	return nil
}

func (p *PodmanApplication) logPodsToStart(podsToStart []types.Pod) {
	logger.Infof("Found %d pods for given applicationName.\n", len(podsToStart))
	logger.Infoln("Below pods will be started:")
	for _, pod := range podsToStart {
		logger.Infof("\t-> %s\n", pod.Name)
	}
}

func (p *PodmanApplication) shouldPrintLogs(podsToStart []types.Pod, skipLogs bool) bool {
	if len(podsToStart) != 1 || skipLogs {
		return false
	}
	logger.Infoln("Note: After starting the pod, logs will be displayed. Press Ctrl+C to exit the logs and return to the terminal.")
	return true
}

func (p *PodmanApplication) startPods(podsToStart []types.Pod) error {
	var errors []string
	for _, pod := range podsToStart {
		logger.Infof("Starting the pod: %s\n", pod.Name)
		podData, err := p.runtime.InspectPod(pod.Name)
		if err != nil {
			errMsg := fmt.Sprintf("%s: %v", pod.Name, err)
			errors = append(errors, errMsg)
			continue
		}

		if podData.State == "Running" {
			logger.Infof("Pod %s is already running. Skipping...\n", pod.Name)
			continue
		}
		if err := p.runtime.StartPod(pod.ID); err != nil {
			errMsg := fmt.Sprintf("%s: %v", pod.Name, err)
			errors = append(errors, errMsg)
			continue
		}

		logger.Infof("Successfully started the pod: %s\n", pod.Name)
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to start pods: \n%s", strings.Join(errors, "\n"))
	}

	return nil
}

func (p *PodmanApplication) printPodLogs(podsToStart []types.Pod) error {
	logger.Infof("\n--- Following logs for pod: %s ---\n", podsToStart[0].Name)

	if err := p.runtime.PodLogs(podsToStart[0].Name); err != nil {
		if strings.Contains(err.Error(), "signal: interrupt") || strings.Contains(err.Error(), "context canceled") {
			logger.Infoln("Log following stopped.")
			return nil
		}
		return fmt.Errorf("failed to follow logs for pod %s: %w", podsToStart[0].Name, err)
	}

	return nil
}

func (p *PodmanApplication) filterPodsByNameForStart(pods []types.Pod, podNames []string) ([]types.Pod, error) {
	podMap := make(map[string]types.Pod)
	for _, pod := range pods {
		podMap[pod.Name] = pod
	}

	var notFound []string
	var podsToStart []types.Pod
	for _, podName := range podNames {
		if pod, exists := podMap[podName]; exists {
			podsToStart = append(podsToStart, pod)
		} else {
			notFound = append(notFound, podName)
		}
	}

	if len(notFound) > 0 {
		logger.Warningf("The following specified pods were not found and will be skipped: %s\n", strings.Join(notFound, ", "))
	}

	return podsToStart, nil
}

func (p *PodmanApplication) filterPodsByAnnotationForStart(pods []types.Pod) ([]types.Pod, error) {
	var podsToStart []types.Pod

outerloop:
	for _, pod := range pods {
		for _, container := range pod.Containers {
			data, err := p.runtime.InspectContainer(container.Name)
			if err != nil {
				return podsToStart, fmt.Errorf("failed to inspect container %s: %w", container.Name, err)
			}
			annotations := data.Annotations
			if val, exists := annotations[constants.PodStartAnnotationkey]; exists && val == constants.PodStartOff {
				continue outerloop
			}
		}
		podsToStart = append(podsToStart, pod)
	}

	return podsToStart, nil
}

// Stop stops a running application
func (p *PodmanApplication) Stop(opts StopOptions) error {
	pods, err := p.runtime.ListPods(map[string][]string{
		"label": {fmt.Sprintf("ai-services.io/application=%s", opts.Name)},
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods) == 0 {
		logger.Infof("No pods found with given application: %s\n", opts.Name)
		return nil
	}

	// Filter pods based on provided pod names
	podsToStop, err := p.fetchPodsToStop(pods, opts.PodNames, opts.Name)
	if err != nil {
		return err
	}

	if len(podsToStop) == 0 {
		logger.Infof("Invalid/No pods found to stop for given application: %s\n", opts.Name)
		return nil
	}

	logger.Infof("Found %d pods for given applicationName: %s.\n", len(podsToStop), opts.Name)
	logger.Infoln("Below pods will be stopped:")
	for _, pod := range podsToStop {
		logger.Infof("\t-> %s\n", pod.Name)
	}

	if !opts.AutoYes {
		confirmStop, err := utils.ConfirmAction("Are you sure you want to stop the above pods? ")
		if err != nil {
			return fmt.Errorf("failed to take user input: %w", err)
		}

		if !confirmStop {
			logger.Infof("Skipping stopping of pods\n")
			return nil
		}
	}

	logger.Infof("Proceeding to stop pods...\n")

	return p.stopPods(podsToStop)
}

func (p *PodmanApplication) fetchPodsToStop(pods []types.Pod, podNames []string, appName string) ([]types.Pod, error) {
	var podsToStop []types.Pod
	if len(podNames) > 0 {
		// Filter pods
		podMap := make(map[string]types.Pod)
		for _, pod := range pods {
			podMap[pod.Name] = pod
		}

		// maintain list of not found pod names
		var notFound []string
		for _, podname := range podNames {
			if pod, exists := podMap[podname]; exists {
				podsToStop = append(podsToStop, pod)
			} else {
				notFound = append(notFound, podname)
			}
		}

		// Warn if any provided pod names do not exist
		if len(notFound) > 0 {
			logger.Warningf("The following specified pods were not found and will be skipped: %s\n", strings.Join(notFound, ", "))
		}
	} else {
		// No specific pod names provided, stop all pods
		podsToStop = pods
	}

	return podsToStop, nil
}

func (p *PodmanApplication) stopPods(podsToStop []types.Pod) error {
	var errors []string
	for _, pod := range podsToStop {
		logger.Infof("Stopping the pod: %s\n", pod.Name)

		if err := p.runtime.StopPod(pod.ID); err != nil {
			errMsg := fmt.Sprintf("%s: %v", pod.Name, err)
			errors = append(errors, errMsg)
			continue
		}

		logger.Infof("Successfully stopped the pod: %s\n", pod.Name)
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to stop pods: \n%s", strings.Join(errors, "\n"))
	}

	return nil
}

// List returns information about running applications
func (p *PodmanApplication) List(opts ListOptions) ([]ApplicationInfo, error) {
	// Set the output wide flag
	p.isOutputWide = opts.OutputWide
	
	// filter and fetch pods based on appName
	pods, err := p.fetchFilteredPods(opts.ApplicationName)
	if err != nil {
		return nil, err
	}

	// if no pods are present and also if appName is provided then simply log and return
	if len(pods) == 0 && opts.ApplicationName != "" {
		logger.Infof("No Pods found for the given application name: %s", opts.ApplicationName)
		return nil, nil
	}

	// fetch the table writer object
	printer := utils.NewTableWriter()
	defer printer.CloseTableWriter()

	// set table headers
	p.setTableHeaders(printer, opts.OutputWide)

	// render each pod info as rows in the table
	p.renderPodRows(printer, pods)

	return nil, nil
}

func (p *PodmanApplication) fetchFilteredPods(appName string) ([]types.Pod, error) {
	listFilters := map[string][]string{}
	if appName != "" {
		listFilters["label"] = []string{fmt.Sprintf("ai-services.io/application=%s", appName)}
	}

	pods, err := p.runtime.ListPods(listFilters)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	return pods, nil
}

func (p *PodmanApplication) setTableHeaders(printer *utils.Printer, outputWide bool) {
	if outputWide {
		printer.SetHeaders("APPLICATION NAME", "POD ID", "POD NAME", "STATUS", "CREATED", "EXPOSED", "CONTAINERS")
	} else {
		printer.SetHeaders("APPLICATION NAME", "POD NAME", "STATUS")
	}
}

func (p *PodmanApplication) renderPodRows(printer *utils.Printer, pods []types.Pod) {
	for _, pod := range pods {
		p.processAndAppendPodRow(printer, pod)
	}
}

func (p *PodmanApplication) processAndAppendPodRow(printer *utils.Printer, pod types.Pod) {
	appName := p.fetchPodNameFromLabels(pod.Labels)
	if appName == "" {
		// skip pods which are not linked to ai-services
		return
	}

	// do pod inspect
	pInfo, err := p.runtime.InspectPod(pod.ID)
	if err != nil {
		// log and skip pod if inspect failed
		logger.Errorf("Failed to do pod inspect: '%s' with error: %v", pod.ID, err)
		return
	}

	// fetch pod row
	rows := p.buildPodRow(appName, pInfo)
	// append pod row to the table
	printer.AppendRow(rows...)
}

func (p *PodmanApplication) buildPodRow(appName string, pod *types.Pod) []string {
	status := p.getPodStatus(pod)

	// if wide option flag is not set, then return appName, podName and status only
	if !p.isOutputWide {
		return []string{appName, pod.Name, status}
	}

	containerNames := p.getContainerNames(pod)

	podPorts, err := p.getPodPorts(pod)
	if err != nil {
		podPorts = []string{"none"}
	}

	return []string{
		appName,
		pod.ID[:12],
		pod.Name,
		status,
		utils.TimeAgo(pod.Created),
		strings.Join(podPorts, ", "),
		strings.Join(containerNames, ", "),
	}
}

func (p *PodmanApplication) getPodPorts(pInfo *types.Pod) ([]string, error) {
	podPorts := []string{}

	if pInfo.Ports != nil {
		for _, hostPorts := range pInfo.Ports {
			podPorts = append(podPorts, hostPorts...)
		}
	}

	if len(podPorts) == 0 {
		podPorts = []string{"none"}
	}

	return podPorts, nil
}

func (p *PodmanApplication) fetchPodNameFromLabels(labels map[string]string) string {
	return labels[constants.ApplicationAnnotationKey]
}

func (p *PodmanApplication) getContainerNames(pod *types.Pod) []string {
	containerNames := []string{}

	for _, container := range pod.Containers {
		cInfo, err := p.runtime.InspectContainer(container.ID)
		if err != nil {
			// skip container if inspect failed
			logger.Infof("failed to do container inspect for pod: '%s', containerID: '%s' with error: %v", pod.Name, container.ID, err, logger.VerbosityLevelDebug)
			continue
		}

		// Along with container name append the container status too
		status := p.fetchContainerStatus(cInfo)
		cInfo.Name += fmt.Sprintf(" (%s)", status)

		containerNames = append(containerNames, cInfo.Name)
	}

	if len(containerNames) == 0 {
		containerNames = []string{"none"}
	}

	return containerNames
}

func (p *PodmanApplication) getPodStatus(pInfo *types.Pod) string {
	// if the pod Status is running, make sure to check if its healthy or not, otherwise fallback to default pod state
	if pInfo.State == "Running" {
		healthyContainers := 0
		for _, container := range pInfo.Containers {
			cInfo, err := p.runtime.InspectContainer(container.ID)
			if err != nil {
				// skip container if inspect failed
				logger.Infof("failed to do container inspect for pod: '%s', containerID: '%s' with error: %v", pInfo.Name, container.ID, err, logger.VerbosityLevelDebug)
				continue
			}

			status := p.fetchContainerStatus(cInfo)
			if status == string(constants.Ready) {
				healthyContainers++
			}
		}

		// if all the containers are healthy, then append 'healthy' to pod state or else mark it as unhealthy
		if healthyContainers == len(pInfo.Containers) {
			pInfo.State += fmt.Sprintf(" (%s)", constants.Ready)
		} else {
			pInfo.State += fmt.Sprintf(" (%s)", constants.NotReady)
		}
	}

	return pInfo.State
}

func (p *PodmanApplication) fetchContainerStatus(cInfo *types.Container) string {
	containerStatus := cInfo.Status

	// if container status is not running, then return the container status
	if containerStatus != "running" {
		return containerStatus
	}

	// if running, proceed with checking health status of the container
	healthStatusCheck := cInfo.Health

	// if health status check is set, then return the particular health status
	if healthStatusCheck != "" {
		return healthStatusCheck
	}

	// if health status check is not set, consider it to be healthy by default
	return string(constants.Ready)
}

// Info displays detailed information about an application
func (p *PodmanApplication) Info(opts InfoOptions) error {
	// Step1: Do List pods and filter for given application name
	listFilters := map[string][]string{}
	if opts.Name != "" {
		listFilters["label"] = []string{fmt.Sprintf("ai-services.io/application=%s", opts.Name)}
	}

	pods, err := p.runtime.ListPods(listFilters)
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	// If there exists no pod for given application name, then fail saying application for given application name doesnt exist
	if len(pods) == 0 {
		logger.Infof("Application: '%s' does not exist.", opts.Name)
		return nil
	}

	logger.Infoln("Application Name: " + opts.Name)

	// Step2: From one of the pod, fetch and print the template and version label values
	appTemplate := pods[0].Labels[string(vars.TemplateLabel)]
	logger.Infoln("Application Template: " + appTemplate)

	version := pods[0].Labels[string(vars.VersionLabel)]
	logger.Infoln("Version: " + version)

	// Step3: Read and print the info.md file
	if err := helpers.PrintInfo(p.runtime, opts.Name, appTemplate); err != nil {
		// not failing if overall info command, if we cannot display Info
		logger.Errorf("failed to display info: %v\n", err)
		return nil
	}

	return nil
}

// Logs displays logs from an application pod
func (p *PodmanApplication) Logs(opts LogsOptions) error {
	logger.Warningln("Press Ctrl+C to exit the logs and return to the terminal.")
	logger.Infof("Fetching logs for application pod: %s", opts.PodName)

	if opts.ContainerNameOrID == "" {
		if err := p.runtime.PodLogs(opts.PodName); err != nil {
			return fmt.Errorf("failed to fetch pod: %s logs; err: %w", opts.PodName, err)
		}
		return nil
	}

	// Fetch container logs
	exists, err := p.runtime.ContainerExists(opts.ContainerNameOrID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("container %s doesn't exists", opts.ContainerNameOrID)
	}
	
	logger.Infof("Fetching logs for container: %s", opts.ContainerNameOrID)
	if err := p.runtime.ContainerLogs(opts.ContainerNameOrID); err != nil {
		return fmt.Errorf("failed to fetch container: %s logs; err: %w", opts.ContainerNameOrID, err)
	}

	return nil
}

// Helper methods for Create

func (p *PodmanApplication) downloadImagesForTemplate(templateName, appName string, imagePullPolicy image.ImagePullPolicy) error {
	// create a new imagePull object based on imagePullPolicy
	imagePull := image.NewImagePull(p.runtime, imagePullPolicy, appName, templateName)

	// based on the imagePullPolicy set, download the images
	return imagePull.Run()
}

func (p *PodmanApplication) getSMTLevel(output string) (int, error) {
	out := strings.TrimSpace(output)

	if !strings.HasPrefix(out, "SMT=") {
		return 0, fmt.Errorf("unexpected output: %s", out)
	}

	SMTLevelStr := strings.TrimPrefix(out, "SMT=")
	SMTlevel, err := strconv.Atoi(SMTLevelStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse SMT level: %w", err)
	}

	return SMTlevel, nil
}

func (p *PodmanApplication) setSMTLevel(templateName string) error {
	// 1. Fetch Current SMT level
	cmd := exec.Command("ppc64_cpu", "--smt")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to check current SMT level: %v, output: %s", err, string(out))
	}

	currentSMTlevel, err := p.getSMTLevel(string(out))
	if err != nil {
		return fmt.Errorf("failed to get current SMT level: %w", err)
	}

	// 2. Fetch the target SMT level
	targetSMTLevel, err := p.getTargetSMTLevel(templateName)
	if err != nil {
		return fmt.Errorf("failed to get target SMT level: %w", err)
	}

	if targetSMTLevel == nil {
		// No SMT level specified in metadata.yaml
		logger.Infof("No SMT level specified in metadata.yaml. Keeping it to current level: %d\n", currentSMTlevel)
		return nil
	}

	// 3. Check if SMT level is already set to target value
	if currentSMTlevel == *targetSMTLevel {
		// already set
		logger.Infof("SMT level is already set to %d\n", *targetSMTLevel)
		return nil
	}

	// 4. Set SMT level to target value
	arg := "--smt=" + strconv.Itoa(*targetSMTLevel)
	cmd = exec.Command("ppc64_cpu", arg)
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set SMT level: %v, output: %s", err, string(out))
	}

	// 5. Verify again
	cmd = exec.Command("ppc64_cpu", "--smt")
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to verify SMT level: %v, output: %s", err, string(out))
	}

	currentSMTlevel, err = p.getSMTLevel(string(out))
	if err != nil {
		return fmt.Errorf("failed to get SMT level after updating: %w", err)
	}

	if currentSMTlevel != *targetSMTLevel {
		return fmt.Errorf("SMT level verification failed: expected %d, got %d", targetSMTLevel, currentSMTlevel)
	}

	return nil
}

func (p *PodmanApplication) getTargetSMTLevel(templateName string) (*int, error) {
	tp := templates.NewEmbedTemplateProvider(templates.EmbedOptions{})

	// validate whether the provided template name is correct
	if err := validators.ValidateAppTemplateExist(tp, templateName); err != nil {
		return nil, err
	}

	// load metadata.yml to read the app metadata
	appMetadata, err := tp.LoadMetadata(templateName, false)
	if err != nil {
		return nil, fmt.Errorf("failed to read the app metadata: %w", err)
	}

	return appMetadata.SMTLevel, nil
}

func (p *PodmanApplication) verifyPodTemplateExists(tmpls map[string]*template.Template, appMetadata *templates.AppMetadata) error {
	flattenPodTemplateExecutions := utils.FlattenArray(appMetadata.PodTemplateExecutions)

	if len(flattenPodTemplateExecutions) != len(tmpls) {
		return errors.New("number of values specified in podTemplateExecutions under metadata.yml is mismatched. Please ensure all the pod template file names are specified")
	}

	// Make sure the podTemplateExecution mentioned in metadata.yaml is valid (corresponding pod template is present)
	for _, podTemplate := range flattenPodTemplateExecutions {
		if _, ok := tmpls[podTemplate]; !ok {
			return fmt.Errorf("value: %s specified in podTemplateExecutions under metadata.yml is invalid. Please ensure corresponding template file exists", podTemplate)
		}
	}

	return nil
}

func (p *PodmanApplication) executePodTemplateLayer(tp templates.Template, tmpls map[string]*template.Template,
	globalParams map[string]any, pciAddresses []string, existingPods []string, podTemplateName, appName string,
	valuesFiles []string, argParams map[string]string) error {
	logger.Infof("'%s': Processing template...\n", podTemplateName)

	// Shallow Copy globalParams Map
	params := utils.CopyMap(globalParams)

	// fetch pod Spec
	podSpec, err := p.fetchPodSpec(tp, globalParams["AppTemplateName"].(string), podTemplateName, appName, valuesFiles, argParams)
	if err != nil {
		return err
	}

	if slices.Contains(existingPods, podSpec.Name) {
		logger.Infof("%s: Skipping pod deploy as '%s' it already exists", podTemplateName, podSpec.Name)
		return nil
	}

	// fetch annotations from pod Spec
	podAnnotations := p.fetchPodAnnotations(podSpec)

	// get the env params for a given pod
	env, err := p.returnEnvParamsForPod(podSpec, podAnnotations, &pciAddresses)
	if err != nil {
		return fmt.Errorf("'%s': Failed to fetch env params: %w", podTemplateName, err)
	}
	params["env"] = env

	podTemplate := tmpls[podTemplateName]

	var rendered bytes.Buffer
	if err := podTemplate.Execute(&rendered, params); err != nil {
		return fmt.Errorf("'%s': Failed to parse pod template: %w", podTemplateName, err)
	}

	// Wrap the bytes in a bytes.Reader
	reader := bytes.NewReader(rendered.Bytes())

	// Deploy the Pod and do Readiness check
	if err := p.deployPodAndReadinessCheck(podSpec, podTemplateName, reader, p.constructPodDeployOptions(podAnnotations)); err != nil {
		return fmt.Errorf("'%s': Failed to deploy pod and do readiness check: %w", podTemplateName, err)
	}

	return nil
}

func (p *PodmanApplication) executePodTemplates(tp templates.Template,
	appName string, appMetadata *templates.AppMetadata,
	tmpls map[string]*template.Template, pciAddresses []string, existingPods []string,
	valuesFiles []string, argParams map[string]string) error {
	
	// Load values for template rendering
	values, err := tp.LoadValues(appMetadata.Name, valuesFiles, argParams)
	if err != nil {
		return fmt.Errorf("failed to load params for application: %w", err)
	}

	globalParams := map[string]any{
		"AppName":         appName,
		"AppTemplateName": appMetadata.Name,
		"Version":         appMetadata.Version,
		"Values":          values,
		// Key -> container name
		// Value -> range of key-value env pairs
		"env": map[string]map[string]string{},
	}

	// looping over each layer of podTemplateExecutions
	for i, layer := range appMetadata.PodTemplateExecutions {
		logger.Infof("\n Executing Layer %d/%d: %v\n", i+1, len(appMetadata.PodTemplateExecutions), layer)
		logger.Infoln("-------")
		var wg sync.WaitGroup
		errCh := make(chan error, len(layer))

		// for each layer, fetch all the pod Template Names and do the pod deploy
		for _, podTemplateName := range layer {
			wg.Add(1)
			go func(t string) {
				defer wg.Done()
				if err := p.executePodTemplateLayer(tp, tmpls, globalParams, pciAddresses, existingPods, podTemplateName, appName, valuesFiles, argParams); err != nil {
					errCh <- err
				}
			}(podTemplateName)
		}

		wg.Wait()
		close(errCh)

		// collect all errors for this layer
		var errs []error
		for e := range errCh {
			errs = append(errs, fmt.Errorf("layer %d: %w", i+1, e))
		}

		// If an error exist for a given layer, then return (do not process further layers)
		if len(errs) > 0 {
			return errors.Join(errs...)
		}

		logger.Infof("Layer %d completed\n", i+1)
	}

	return nil
}

func (p *PodmanApplication) doContainersCreationCheck(podSpec *models.PodSpec, podTemplateName, podName, podID string) error {
	logger.Infof("'%s', '%s': Performing Containers Creation check for pod...\n", podTemplateName, podName)

	expectedContainerCount := len(specs.FetchContainerNames(*podSpec))

	logger.Infof("'%s', '%s': Waiting for Containers Creation... Timeout set: %s\n", podTemplateName, podName, containerCreationTimeout)
	// wait for all containers for a given pod are created
	if err := helpers.WaitForContainersCreation(p.runtime, podID, expectedContainerCount, containerCreationTimeout); err != nil {
		return fmt.Errorf("containers creation check failed for pod: '%s' with error: %w", podName, err)
	}

	logger.Infof("'%s', '%s': Containers creation check for pod is completed\n", podTemplateName, podName)

	return nil
}

func (p *PodmanApplication) doContainerReadinessCheck(podTemplateName, podName, containerID string) error {
	cInfo, err := p.runtime.InspectContainer(containerID)
	if err != nil {
		return fmt.Errorf("failed to do container inspect for containerID: '%s' with error: %w", containerID, err)
	}

	logger.Infof("'%s', '%s', '%s': Performing Container Readiness check...\n", podTemplateName, podName, cInfo.Name)

	// getting the Start Period set for a container
	startPeriod, err := helpers.FetchContainerStartPeriod(p.runtime, containerID)
	if err != nil {
		return fmt.Errorf("fetching container: '%s' start period failed: %w", cInfo.Name, err)
	}

	if startPeriod == -1 {
		logger.Infof("No container health check is set for '%s'. Hence skipping readiness check\n", cInfo.Name, logger.VerbosityLevelDebug)
		return nil
	}

	// configure readiness timeout by appending start period with additional extra timeout
	readinessTimeout := startPeriod + extraContainerReadinessTimeout

	logger.Infof("'%s', '%s', '%s': Waiting for Container Readiness... Timeout set: %s\n", podTemplateName, podName, cInfo.Name, readinessTimeout)

	if err := helpers.WaitForContainerReadiness(p.runtime, containerID, readinessTimeout); err != nil {
		return fmt.Errorf("readiness check failed for container: '%s'!: %w", cInfo.Name, err)
	}
	logger.Infof("'%s', '%s', '%s': Readiness Check for the container is completed!\n", podTemplateName, podName, cInfo.Name)

	return nil
}

func (p *PodmanApplication) deployPodAndReadinessCheck(podSpec *models.PodSpec,
	podTemplateName string, body io.Reader, opts map[string]string) error {
	pods, err := p.runtime.CreatePod(body)
	if err != nil {
		return fmt.Errorf("failed pod creation: %w", err)
	}

	logger.Infof("'%s': Successfully ran podman kube play\n", podTemplateName, logger.VerbosityLevelDebug)

	// ---- Pod Readiness Checks ----
	for _, pod := range pods {
		pInfo, err := p.runtime.InspectPod(pod.ID)
		if err != nil {
			return fmt.Errorf("failed to do pod inspect for podID: '%s' with error: %w", pod.ID, err)
		}

		podName := pInfo.Name

		logger.Infof("'%s', '%s': Starting Pod Readiness check...\n", podTemplateName, podName)

		// Step1: ---- Containers Creation Check ----
		if err := p.doContainersCreationCheck(podSpec, podTemplateName, pInfo.Name, pInfo.ID); err != nil {
			return err
		}

		// Step2: ---- Containers Readiness Check ----
		for _, container := range pInfo.Containers {
			if err := p.doContainerReadinessCheck(podTemplateName, pInfo.Name, container.ID); err != nil {
				return err
			}
			logger.Infoln("-------")
		}
		logger.Infof("'%s', '%s': Pod has been successfully deployed and ready!\n", podTemplateName, podName)
		logger.Infoln("-------")
	}

	logger.Infoln("-------\n-------")

	return nil
}

func (p *PodmanApplication) validateSpyreCardRequirements(req int, actual int) error {
	if actual < req {
		return fmt.Errorf("insufficient spyre cards. Require: %d spyre cards to proceed", req)
	}

	return nil
}

func (p *PodmanApplication) calculateReqSpyreCards(tp templates.Template, podTemplateFileNames []string, appTemplateName, appName string) (int, error) {
	totalReqSpyreCounts := 0

	// Calculate Req Spyre Counts
	for _, podTemplateFileName := range podTemplateFileNames {
		// fetch pod spec
		podSpec, err := p.fetchPodSpec(tp, appTemplateName, podTemplateFileName, appName, nil, nil)
		if err != nil {
			return totalReqSpyreCounts, fmt.Errorf("failed to load pod Template: '%s' for appTemplate: '%s' with error: %w", podTemplateFileName, appTemplateName, err)
		}

		// check if pod already exists and skip counting if it does exists
		exists, err := p.runtime.PodExists(podSpec.Name)
		if err != nil {
			return totalReqSpyreCounts, fmt.Errorf("failed to check pod status: %w", err)
		}

		if exists {
			logger.Infof("Pod %s already exists, skipping spyre cards calculation", podSpec.Name, logger.VerbosityLevelDebug)
			continue
		}

		// fetch the spyreCount for all containers from the annotations
		spyreCount, _, err := p.fetchSpyreCardsFromPodAnnotations(podSpec.Annotations)
		if err != nil {
			return totalReqSpyreCounts, err
		}

		totalReqSpyreCounts += spyreCount
	}

	return totalReqSpyreCounts, nil
}

func (p *PodmanApplication) fetchSpyreCardsFromPodAnnotations(annotations map[string]string) (int, map[string]int, error) {
	var spyreCards int
	// spyreCardContainerMap: Key -> containerName, Value -> SpyreCardCounts
	spyreCardContainerMap := map[string]int{}

	isSpyreCardAnnotation := func(annotation string) (string, bool) {
		matches := vars.SpyreCardAnnotationRegex.FindStringSubmatch(annotation)
		if matches == nil {
			return "", false
		}

		return matches[1], true
	}

	for annotationKey, val := range annotations {
		if containerName, ok := isSpyreCardAnnotation(annotationKey); ok {
			valInt, err := strconv.Atoi(val)
			if err != nil {
				return 0, spyreCardContainerMap, fmt.Errorf("failed to convert to int. Provided val: %s is not of int type", val)
			}
			// Replace with container name
			spyreCardContainerMap[containerName] = valInt
			spyreCards += valInt
		}
	}

	return spyreCards, spyreCardContainerMap, nil
}

func (p *PodmanApplication) fetchPodSpec(tp templates.Template, appTemplateName, podTemplateFileName, appName string, valuesFiles []string, argParams map[string]string) (*models.PodSpec, error) {
	podSpec, err := tp.LoadPodTemplateWithValues(appTemplateName, podTemplateFileName, appName, valuesFiles, argParams)
	if err != nil {
		return nil, fmt.Errorf("failed to load pod Template: '%s' for appTemplate: '%s' with error: %w", podTemplateFileName, appTemplateName, err)
	}

	return podSpec, nil
}

func (p *PodmanApplication) fetchPodAnnotations(podSpec *models.PodSpec) map[string]string {
	return specs.FetchPodAnnotations(*podSpec)
}

func (p *PodmanApplication) returnEnvParamsForPod(podSpec *models.PodSpec, podAnnotations map[string]string, pciAddresses *[]string) (map[string]map[string]string, error) {
	env := map[string]map[string]string{}
	podContainerNames := specs.FetchContainerNames(*podSpec)

	// populate env with empty map
	for _, containerName := range podContainerNames {
		env[containerName] = map[string]string{}
	}

	// fetch the spyre cards and spyre card count required for each container in a pod
	spyreCards, spyreCardContainerMap, err := p.fetchSpyreCardsFromPodAnnotations(podAnnotations)
	if err != nil {
		return env, err
	}

	if spyreCards == 0 {
		// The pod doesn't require any spyre cards. // populate the given container with empty map
		return env, nil
	}

	// Construct env for a given pod
	// Since this is a critical section as both requires pciAddresses and modifies -> wrap it in mutex
	envMutex.Lock()
	for container, spyreCount := range spyreCardContainerMap {
		if spyreCount != 0 {
			env[container] = map[string]string{string(constants.PCIAddressKey): utils.JoinAndRemove(pciAddresses, spyreCount, " ")}
		}
	}
	envMutex.Unlock()

	return env, nil
}

func (p *PodmanApplication) checkForPodStartAnnotation(podAnnotations map[string]string) string {
	if val, ok := podAnnotations[constants.PodStartAnnotationkey]; ok {
		if val == constants.PodStartOff || val == constants.PodStartOn {
			return val
		}
	}

	return ""
}

func (p *PodmanApplication) fetchHostPortMappingFromAnnotation(podAnnotations map[string]string) map[string]string {
	// key -> containerPort and value -> hostPort
	hostPortMapping := map[string]string{}

	portMappings, ok := podAnnotations[constants.PodPortsAnnotationKey]
	if !ok {
		// return empty map if port annotation is not present
		return hostPortMapping
	}

	portMapping := strings.SplitSeq(portMappings, ",")
	for p := range portMapping {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		// Find colon
		i := strings.Index(p, ":")
		if i == -1 {
			// No colon → whole thing is the containerPort
			hostPortMapping[p] = ""
			continue
		}

		// Before colon string is hostPort
		hostPort := strings.TrimSpace(p[:i])
		// After colon string is containerPort
		containerPort := strings.TrimSpace(p[i+1:])

		// If colon exists but NO value after the colon (containerPort) → then skip
		if containerPort == "" {
			continue
		}

		hostPortMapping[containerPort] = hostPort
	}

	return hostPortMapping
}

func (p *PodmanApplication) constructPodDeployOptions(podAnnotations map[string]string) map[string]string {
	podStart := p.checkForPodStartAnnotation(podAnnotations)

	// construct start option
	podDeployOptions := map[string]string{}
	if podStart != "" {
		podDeployOptions["start"] = podStart
	}

	// construct publish option
	hostPortMappings := p.fetchHostPortMappingFromAnnotation(podAnnotations)
	podDeployOptions["publish"] = ""

	// loop over each of the hostPortMappings to construct the 'publish' option
	for containerPort, hostPort := range hostPortMappings {
		if hostPort == "0" {
			// if the host port is set to 0, then do not expose the particular containerPort
			continue
		}
		if hostPort != "" {
			// if the host port is present
			podDeployOptions["publish"] += hostPort + ":" + containerPort
		} else {
			// else just populate the containerPort, so that dynamically podman will populate
			podDeployOptions["publish"] += containerPort
		}
		podDeployOptions["publish"] += ","
	}

	return podDeployOptions
}

// Type returns the runtime type
func (p *PodmanApplication) Type() types.RuntimeType {
	return types.RuntimeTypePodman
}

// Made with Bob

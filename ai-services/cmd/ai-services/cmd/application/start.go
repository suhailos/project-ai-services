package application

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/project-ai-services/ai-services/internal/pkg/application"
)

var (
	skipLogs      bool
	startPodNames []string
	autoYes       bool
)

var startCmd = &cobra.Command{
	Use:   "start [name]",
	Short: "Start an application",
	Long: `Starts an application by name.

Arguments
  [name]: Application name (required)

Note: Logs are streamed only when a single pod is specified, and only after the pod has started.
`,
	Args: cobra.ExactArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		var err error
		startPodNames, err = cmd.Flags().GetStringSlice("pod")
		if err != nil {
			return fmt.Errorf("failed to parse --pod flag: %w", err)
		}

		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		applicationName := args[0]

		// Once precheck passes, silence usage for any *later* internal errors.
		cmd.SilenceUsage = true

		// Create application instance
		factory := application.NewFactoryFromEnv()
		app, err := factory.Create()
		if err != nil {
			return fmt.Errorf("failed to create application instance: %w", err)
		}

		// Start application with options
		opts := application.StartOptions{
			Name:     applicationName,
			PodNames: startPodNames,
			SkipLogs: skipLogs,
			AutoYes:  autoYes,
		}

		return app.Start(opts)
	},
}

func init() {
	startCmd.Flags().StringSlice("pod", []string{}, "Specific pod name(s) to start (optional)\nCan be specified multiple times: --pod pod1 --pod pod2\nOr comma-separated: --pod pod1,pod2")
	startCmd.Flags().BoolVar(&skipLogs, "skip-logs", false, "Skip displaying logs after starting the pod")
	startCmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Automatically accept all confirmation prompts (default=false)")
}

// Made with Bob

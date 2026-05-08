package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gsd-build/daemon/internal/lab"
	"github.com/spf13/cobra"
)

var labFlags struct {
	cwd            string
	provider       string
	model          string
	effort         string
	permissionMode string
	fake           bool
	piBinary       string
	warmWorkers    string
	port           int
}

var labCmd = &cobra.Command{
	Use:   "lab",
	Short: "Run the local provider lab",
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd := labFlags.cwd
		if absCWD, err := filepath.Abs(cwd); err == nil {
			cwd = absCWD
		}
		extensionPath := filepath.Join("internal", "pi", "extension", "index.ts")
		if absExtensionPath, err := filepath.Abs(extensionPath); err == nil {
			extensionPath = absExtensionPath
		}
		result := lab.RunPreflight(lab.PreflightOptions{
			CWD:           cwd,
			Provider:      labFlags.provider,
			Model:         labFlags.model,
			FakeMode:      labFlags.fake,
			PiBinary:      labFlags.piBinary,
			ExtensionPath: extensionPath,
		})
		if !result.OK {
			data, _ := json.MarshalIndent(result, "", "  ")
			return fmt.Errorf("provider lab preflight failed:\n%s", string(data))
		}
		store, err := lab.NewSessionStore(lab.SessionStoreOptions{
			RootDir: filepath.Join("/tmp", "gsd-provider-lab"),
			Config: lab.SessionConfig{
				CWD:            cwd,
				Provider:       labFlags.provider,
				Model:          labFlags.model,
				Effort:         labFlags.effort,
				PermissionMode: labFlags.permissionMode,
				FakeMode:       labFlags.fake,
				PiBinary:       labFlags.piBinary,
				WarmWorkers:    parseLabBoolPtr(labFlags.warmWorkers),
			},
		})
		if err != nil {
			return err
		}
		relay := lab.NewLocalRelay(lab.LocalRelayOptions{
			MachineID: "lab-machine",
			AuthToken: "lab-token",
			Store:     store,
		})
		server := lab.NewServer(lab.ServerOptions{Store: store, Relay: relay, CWD: cwd})
		addr := fmt.Sprintf("127.0.0.1:%d", labFlags.port)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		defer listener.Close()

		binaryPath := filepath.Join(store.Dir(), "gsd-cloud-lab-child")
		build := exec.Command("go", "build", "-o", binaryPath, ".")
		build.Stdout = os.Stdout
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("build lab child daemon: %w", err)
		}
		runner := lab.NewRunner(lab.RunnerOptions{
			BinaryPath:    binaryPath,
			SessionDir:    store.Dir(),
			RelayURL:      "ws://" + listener.Addr().String() + "/ws/daemon",
			MachineID:     "lab-machine",
			AuthToken:     "lab-token",
			FakeMode:      labFlags.fake,
			PiBinary:      labFlags.piBinary,
			WarmWorkers:   parseLabBoolPtr(labFlags.warmWorkers),
			ExtensionPath: extensionPath,
		})
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		if err := runner.Start(ctx); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "provider lab: http://%s\n", listener.Addr().String())
		return http.Serve(listener, server)
	},
}

var labAnalyzeCmd = &cobra.Command{
	Use:   "analyze <bundle.json>",
	Short: "Analyze a provider lab export bundle",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		var bundle lab.ExportBundle
		if err := json.Unmarshal(data, &bundle); err != nil {
			return err
		}
		report, err := lab.AnalyzeBundle(bundle)
		if err != nil {
			return err
		}
		out, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	},
}

func init() {
	labCmd.Flags().StringVar(&labFlags.cwd, "cwd", ".", "Project working directory")
	labCmd.Flags().StringVar(&labFlags.provider, "provider", "claude-cli", "Provider id")
	labCmd.Flags().StringVar(&labFlags.model, "model", "claude-sonnet-4-6", "Model id")
	labCmd.Flags().StringVar(&labFlags.effort, "effort", "medium", "Reasoning effort")
	labCmd.Flags().StringVar(&labFlags.permissionMode, "permission-mode", "acceptEdits", "Permission mode")
	labCmd.Flags().BoolVar(&labFlags.fake, "fake", false, "Use fake Pi mode")
	labCmd.Flags().StringVar(&labFlags.piBinary, "pi-binary", "", "Pi binary path for the child daemon")
	labCmd.Flags().StringVar(&labFlags.warmWorkers, "warm-workers", "", "Warm worker setting: on or off")
	labCmd.Flags().IntVar(&labFlags.port, "port", 0, "Local lab port")
	labCmd.AddCommand(labAnalyzeCmd)
	rootCmd.AddCommand(labCmd)
}

func parseLabBoolPtr(value string) *bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		enabled := true
		return &enabled
	case "0", "false", "no", "off":
		enabled := false
		return &enabled
	default:
		return nil
	}
}

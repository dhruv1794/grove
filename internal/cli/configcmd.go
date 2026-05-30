package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"grove/internal/core"
	"grove/internal/grove"
)

// newConfigCmd is `grove config`: show the effective configuration, or
// `grove config init` to scaffold a global or local config file.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or initialize grove configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := openGrove(context.Background())
			if err != nil {
				return err
			}
			defer g.Close()
			fmt.Fprintln(cmd.OutOrStdout(), configReport(g))
			return nil
		},
	}
	cmd.AddCommand(newConfigInitCmd())
	return cmd
}

func newConfigInitCmd() *cobra.Command {
	var local, force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a config file seeded from the current effective settings",
		Long: "Writes a config.toml you can edit. By default it writes the global " +
			"~/.grove/config.toml, which applies to every forest. --local writes the " +
			"current workspace's config instead, so this forest can override the global " +
			"default. The file is seeded with the models currently in effect.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := openGrove(context.Background())
			if err != nil {
				return err
			}
			defer g.Close()
			eff, _ := core.LoadMergedConfig(g.ConfigPath())

			target := g.ConfigPath()
			if !local {
				if target, err = core.GlobalConfigPath(); err != nil {
					return err
				}
			}
			// The local config.toml always exists (grove init writes a skeleton as
			// the workspace marker), so guard on whether models are already defined
			// rather than on file existence.
			existing, _ := core.LoadRawConfig(target)
			if (existing.Build.Model != "" || existing.Query.Model != "") && !force {
				return fmt.Errorf("%s already defines models (use --force to overwrite)", target)
			}
			// Render the full annotated template seeded from the effective
			// config, then write atomically. Going through the TOML encoder
			// would drop the inline setup instructions; we'd rather hand-write.
			seeded := eff
			if seeded.Workspace.SchemaVersion == 0 {
				seeded.Workspace.SchemaVersion = 1
			}
			body := core.RenderConfigTemplate(seeded)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", target)
			return nil
		},
	}
	cmd.Flags().BoolVar(&local, "local", false, "write the workspace-local config instead of the global one")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}

// isConfigCmd reports whether line is a /config command, with its optional
// subcommand ("open"/"edit"). Like /workspace it reaches config + the OS, so
// adapters handle it rather than the pure handleReplCommand.
func isConfigCmd(line string) (sub string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	if c := "/" + strings.TrimLeft(fields[0], "/:"); c == "/config" {
		return strings.ToLower(strings.Join(fields[1:], " ")), true
	}
	return "", false
}

// configReport renders the workspace's config: file location, resolved models
// (file + env override), and which provider credentials are present. Secrets
// are never printed — only set/unset.
func configReport(g *grove.Grove) string {
	local := g.ConfigPath()
	global, _ := core.GlobalConfigPath()
	cfg, _ := core.LoadMergedConfig(local) // global ⊕ local ⊕ env; missing files skipped

	mark := func(set bool) string {
		if set {
			return "set"
		}
		return "— not set"
	}
	val := func(s string) string {
		if s == "" {
			return "— (none; --model required)"
		}
		return s
	}
	embed := os.Getenv("GROVE_EMBED_MODEL")
	if embed == "" {
		embed = "ollama/bge-m3 (default)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "workspace:    %s\n", g.Layout().Root)
	fmt.Fprintf(&b, "global config: %s%s\n", global, fileSuffix(global))
	fmt.Fprintf(&b, "local config:  %s%s\n", local, fileSuffix(local))
	fmt.Fprintf(&b, "build model: %s\n", val(cfg.Build.Model))
	fmt.Fprintf(&b, "query model: %s\n", val(cfg.Query.Model))
	fmt.Fprintf(&b, "embed model: %s\n", embed)
	fmt.Fprintf(&b, "keys:\n")
	fmt.Fprintf(&b, "  OpenAI     (GROVE_OPENAI_API_KEY)    %s\n", mark(os.Getenv("GROVE_OPENAI_API_KEY") != ""))
	fmt.Fprintf(&b, "  Anthropic  (GROVE_ANTHROPIC_API_KEY) %s\n", mark(os.Getenv("GROVE_ANTHROPIC_API_KEY") != ""))
	fmt.Fprintf(&b, "  DeepSeek   (GROVE_DEEPSEEK_API_KEY)  %s\n", mark(os.Getenv("GROVE_DEEPSEEK_API_KEY") != ""))
	host := os.Getenv("GROVE_OLLAMA_HOST")
	if host == "" {
		host = "http://localhost:11434 (default)"
	}
	fmt.Fprintf(&b, "  Ollama host (GROVE_OLLAMA_HOST)      %s\n", host)
	fmt.Fprintf(&b, "(models resolve local > global > built-in; `/config open` edits the local file)")
	return b.String()
}

func fileSuffix(path string) string {
	if _, err := os.Stat(path); err != nil {
		return "  (does not exist yet)"
	}
	return ""
}

// configEditorCommand builds the command to open the config file: $EDITOR if
// set, else the platform opener (macOS `open`, Linux `xdg-open`). nil when no
// opener is available. The caller runs it (blocking via tea.ExecProcess in the
// TUI, or directly in the line-reader).
func configEditorCommand(path string) *exec.Cmd {
	if ed := strings.TrimSpace(os.Getenv("EDITOR")); ed != "" {
		parts := strings.Fields(ed)
		return exec.Command(parts[0], append(parts[1:], path)...)
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path)
	case "linux":
		return exec.Command("xdg-open", path)
	}
	return nil
}

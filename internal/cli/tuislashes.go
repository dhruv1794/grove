package cli

import (
	"os"
	"os/exec"
)

// subprocess builds a `grove <args>` re-exec inheriting the workspace/config
// flags. Used for any TUI slash command whose CLI sibling has its own progress
// bar or interactive output (so suspending the TUI gives a clean TTY).
func subprocess(workspace, configPath string, argv ...string) *exec.Cmd {
	pre := []string{"--workspace", workspace}
	if configPath != "" {
		pre = append(pre, "--config", configPath)
	}
	cmd := exec.Command(os.Args[0], append(pre, argv...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

// syncCmdArgs builds the argv for a TUI `/sync` re-exec.
func syncCmdArgs(a syncArgs) []string {
	argv := []string{"sync"}
	if a.Source != "" {
		argv = append(argv, "--source", a.Source)
	}
	if a.Force {
		argv = append(argv, "--force")
	}
	return argv
}

// embedCmdArgs builds the argv for a TUI `/embed` re-exec.
func embedCmdArgs(a embedArgs) []string {
	argv := []string{"embed"}
	if a.Source != "" {
		argv = append(argv, "--source", a.Source)
	}
	if a.Model != "" {
		argv = append(argv, "--model", a.Model)
	}
	if a.Chunks {
		argv = append(argv, "--chunks")
	}
	return argv
}

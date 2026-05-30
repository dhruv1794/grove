package cli

import (
	"os"
	"os/exec"
)

// connectSubprocess builds the command the TUI suspends to in order to run a
// /connect. Re-execing the same grove binary keeps the connect flow exactly
// the same as the bare CLI — progress bar to stderr, OAuth browser flow to a
// clean TTY — without having to teach the bubbletea model to host either.
// SQLite WAL + the file-based token store make the two processes
// concurrency-safe; on return the TUI re-reads sources from the store.
func connectSubprocess(workspace, configPath string, a connectArgs) *exec.Cmd {
	argv := []string{"--workspace", workspace}
	if configPath != "" {
		argv = append(argv, "--config", configPath)
	}
	argv = append(argv, "connect", a.Type)
	if a.Name != "" {
		argv = append(argv, "--name", a.Name)
	}
	if a.Collection != "" {
		argv = append(argv, "--collection", a.Collection)
	}
	if a.AndSync {
		argv = append(argv, "--and-sync")
	}
	switch a.Type {
	case "local", "obsidian":
		argv = append(argv, a.Path)
	case "gdrive":
		if a.FolderID != "" {
			argv = append(argv, "--folder", a.FolderID)
		}
	case "confluence":
		if a.SpaceKey != "" {
			argv = append(argv, "--space", a.SpaceKey)
		}
		if a.Site != "" {
			argv = append(argv, "--site", a.Site)
		}
	}
	cmd := exec.Command(os.Args[0], argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

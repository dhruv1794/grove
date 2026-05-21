package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grove/internal/core"
)

// runCLI builds a fresh root command, executes it with args, and returns
// captured stdout, stderr, and the command error. NewRootCmd rebinds the
// gflags singleton (resetting flag defaults), so each call starts clean; the
// cleanup is belt-and-suspenders for any test that sets gflags directly.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Cleanup(func() { gflags = globalFlags{} })
	root := NewRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// End-to-end happy path mirroring the CLAUDE.md smoke flow: init → connect a
// local folder → status / sources reflect it → --json status is valid.
func TestCLI_SmokeFlow(t *testing.T) {
	ws := t.TempDir()
	docs := t.TempDir()
	if err := os.WriteFile(filepath.Join(docs, "a.md"), []byte("# Hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "b.md"), []byte("# Second\nnote\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := runCLI(t, "--workspace", ws, "init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out, "initialized grove workspace at "+ws) {
		t.Errorf("init output = %q", out)
	}

	// Fresh workspace: status reports no sources.
	out, _, err = runCLI(t, "--workspace", ws, "status")
	if err != nil {
		t.Fatalf("status (empty): %v", err)
	}
	if !strings.Contains(out, "sources:   (none") {
		t.Errorf("empty status output = %q", out)
	}

	out, _, err = runCLI(t, "--workspace", ws, "connect", "local", docs, "--name", "notes")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !strings.Contains(out, `connected local source "notes"`) || !strings.Contains(out, "2 docs indexed") {
		t.Errorf("connect output = %q", out)
	}

	// status now lists the source with its doc count.
	out, _, err = runCLI(t, "--workspace", ws, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "notes") || !strings.Contains(out, "local") {
		t.Errorf("status output missing source: %q", out)
	}

	// sources lists it too.
	out, _, err = runCLI(t, "--workspace", ws, "sources")
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if !strings.Contains(out, "notes") {
		t.Errorf("sources output = %q", out)
	}

	// --json status is valid JSON with the expected shape.
	out, _, err = runCLI(t, "--workspace", ws, "--json", "status")
	if err != nil {
		t.Fatalf("json status: %v", err)
	}
	var report statusReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("status --json is not valid JSON: %v\n%s", err, out)
	}
	if report.Workspace != ws {
		t.Errorf("json workspace = %q, want %q", report.Workspace, ws)
	}
	if len(report.Sources) != 1 || report.Sources[0].Name != "notes" || report.Sources[0].DocCount != 2 {
		t.Errorf("json sources = %+v, want one source 'notes' with 2 docs", report.Sources)
	}
}

func TestCLI_Version(t *testing.T) {
	out, _, err := runCLI(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "grove "+Version) {
		t.Errorf("version output = %q, want it to contain %q", out, "grove "+Version)
	}

	out, _, err = runCLI(t, "--json", "version")
	if err != nil {
		t.Fatalf("json version: %v", err)
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("version --json invalid: %v\n%s", err, out)
	}
	if v["version"] != Version {
		t.Errorf("json version = %q, want %q", v["version"], Version)
	}
}

// A command needing a workspace that hasn't been init'd surfaces a
// no_workspace *core.Error (exit code 3), not a generic failure.
func TestCLI_StatusWithoutInit(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "uninitialized")
	_, _, err := runCLI(t, "--workspace", ws, "status")
	if err == nil {
		t.Fatal("expected an error for an uninitialized workspace, got nil")
	}
	var ge *core.Error
	if !errors.As(err, &ge) {
		t.Fatalf("error is not a *core.Error: %v", err)
	}
	if ge.Kind != core.KindNoWorkspace {
		t.Errorf("error kind = %q, want %q", ge.Kind, core.KindNoWorkspace)
	}
	if ge.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", ge.ExitCode())
	}
}

// Unknown subcommands fail rather than silently succeed.
func TestCLI_UnknownCommand(t *testing.T) {
	if _, _, err := runCLI(t, "frobnicate"); err == nil {
		t.Error("expected error for unknown command, got nil")
	}
}

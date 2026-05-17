package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"grove/internal/core"
)

func TestRenderError_PlainText(t *testing.T) {
	t.Cleanup(func() { gflags.JSON = false })
	gflags.JSON = false

	var buf bytes.Buffer
	code := renderError(&buf, false, core.NewError(core.KindNoWorkspace,
		"no grove workspace at /tmp/x", "run `grove init`"))

	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	out := buf.String()
	if !strings.Contains(out, "error: no grove workspace at /tmp/x") {
		t.Errorf("missing error line: %q", out)
	}
	if !strings.Contains(out, "hint: run `grove init`") {
		t.Errorf("missing hint line: %q", out)
	}
}

func TestRenderError_JSON(t *testing.T) {
	t.Cleanup(func() { gflags.JSON = false })
	gflags.JSON = true

	var buf bytes.Buffer
	cause := errors.New("file is not a database")
	code := renderError(&buf, false, core.WrapError(core.KindCorruptedWorkspace, cause,
		"cannot open workspace database", "re-run `grove init`"))

	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	var je jsonError
	if err := json.Unmarshal(buf.Bytes(), &je); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if je.Code != "corrupted_workspace" {
		t.Errorf("code = %q, want corrupted_workspace", je.Code)
	}
	if !strings.Contains(je.Message, "file is not a database") {
		t.Errorf("message dropped the wrapped cause: %q", je.Message)
	}
	if je.Remediation != "re-run `grove init`" {
		t.Errorf("remediation = %q", je.Remediation)
	}
}

// A non-grove error renders as generic and exits 1.
func TestRenderError_GenericFallback(t *testing.T) {
	t.Cleanup(func() { gflags.JSON = false })
	gflags.JSON = false

	var buf bytes.Buffer
	code := renderError(&buf, false, errors.New("something broke"))

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "error: something broke") {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

// With color enabled, plain-text labels carry ANSI codes; with it off they
// must not (so piped/redirected output stays clean).
func TestRenderError_ColorGating(t *testing.T) {
	t.Cleanup(func() { gflags.JSON = false })
	gflags.JSON = false
	ge := core.NewError(core.KindNoWorkspace, "no workspace", "run init")

	var on bytes.Buffer
	renderError(&on, true, ge)
	if !strings.Contains(on.String(), ansiRed) {
		t.Errorf("color on: missing ANSI code in %q", on.String())
	}

	var off bytes.Buffer
	renderError(&off, false, ge)
	if strings.Contains(off.String(), "\x1b[") {
		t.Errorf("color off: ANSI code leaked into %q", off.String())
	}
}

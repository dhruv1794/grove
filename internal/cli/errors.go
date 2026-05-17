package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"grove/internal/core"
)

// jsonError is the --json error envelope (documents/02-cli-spec.md
// §"Output principles": structured {code, message, remediation} to stderr).
type jsonError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

// Run executes the grove CLI and returns the process exit code.
func Run() int {
	if err := NewRootCmd().Execute(); err != nil {
		return renderError(os.Stderr, useColor(os.Stderr), err)
	}
	return 0
}

// renderError writes err to w — structured JSON when --json is set, plain
// text otherwise — and returns the exit code. Non-grove errors render as a
// generic error (exit 1). When color is true, plain-text labels are tinted.
func renderError(w io.Writer, color bool, err error) int {
	var ge *core.Error
	if !errors.As(err, &ge) {
		ge = core.NewError(core.KindGeneric, err.Error(), "")
	}
	if gflags.JSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(jsonError{
			Code:        string(ge.Kind),
			Message:     ge.Error(),
			Remediation: ge.Remediation,
		}); encErr != nil {
			fmt.Fprintln(w, "error:", ge.Error())
		}
	} else {
		fmt.Fprintln(w, colorize(color, ansiRed, "error:"), ge.Error())
		if ge.Remediation != "" {
			fmt.Fprintln(w, colorize(color, ansiDim, "hint:"), ge.Remediation)
		}
	}
	return ge.ExitCode()
}

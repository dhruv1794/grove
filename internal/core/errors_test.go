package core

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorKind_ExitCode(t *testing.T) {
	cases := map[ErrorKind]int{
		KindGeneric:            1,
		KindMisuse:             2,
		KindNoWorkspace:        3,
		KindNoSources:          4,
		KindModelUnreachable:   5,
		KindSourceUnreachable:  6,
		KindCorruptedWorkspace: 7,
		ErrorKind("bogus"):     1,
	}
	for kind, want := range cases {
		if got := kind.ExitCode(); got != want {
			t.Errorf("%q.ExitCode() = %d, want %d", kind, got, want)
		}
	}
}

func TestError_MessageAndUnwrap(t *testing.T) {
	plain := NewError(KindNoWorkspace, "no workspace here", "run init")
	if plain.Error() != "no workspace here" {
		t.Errorf("plain Error() = %q", plain.Error())
	}
	if plain.Unwrap() != nil {
		t.Errorf("plain Unwrap() = %v, want nil", plain.Unwrap())
	}

	cause := errors.New("disk on fire")
	wrapped := WrapError(KindCorruptedWorkspace, cause, "cannot open db", "re-run init")
	if wrapped.Error() != "cannot open db: disk on fire" {
		t.Errorf("wrapped Error() = %q", wrapped.Error())
	}
	if !errors.Is(wrapped, cause) {
		t.Errorf("errors.Is did not reach the wrapped cause")
	}
}

// A *core.Error must stay recoverable via errors.As after further wrapping.
func TestError_AsThroughWrapping(t *testing.T) {
	ge := NewError(KindCorruptedWorkspace, "bad workspace", "fix it")
	outer := fmt.Errorf("openStore: %w", ge)

	var got *Error
	if !errors.As(outer, &got) {
		t.Fatal("errors.As failed to recover *core.Error")
	}
	if got.ExitCode() != 7 {
		t.Errorf("recovered ExitCode() = %d, want 7", got.ExitCode())
	}
}

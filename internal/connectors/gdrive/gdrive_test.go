package gdrive

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"grove/internal/connectors"
	"grove/internal/core"
)

// fakeDrive is an in-memory driveAPI for tests.
type fakeDrive struct {
	folders  []driveFile // mimeType == mimeFolder
	files    []driveFile
	exports  map[string]map[string][]byte // fileID → mimeType → bytes
	binaries map[string][]byte            // fileID → bytes
	calls    int                          // total api calls (for cheap-list assertions)
}

func (f *fakeDrive) listFiles(_ context.Context, q string) ([]driveFile, error) {
	f.calls++
	since := parseSinceFromQuery(q)
	folderOnly := strings.Contains(q, fmt.Sprintf("mimeType = '%s'", mimeFolder))
	out := []driveFile{}
	src := f.files
	if folderOnly {
		src = f.folders
	}
	for _, df := range src {
		if !since.IsZero() && !df.ModifiedTime.After(since) {
			continue
		}
		out = append(out, df)
	}
	return out, nil
}

func (f *fakeDrive) export(_ context.Context, fileID, mimeType string) ([]byte, error) {
	f.calls++
	if e, ok := f.exports[fileID]; ok {
		if b, ok := e[mimeType]; ok {
			return b, nil
		}
	}
	return nil, fmt.Errorf("no export %s/%s", fileID, mimeType)
}

func (f *fakeDrive) download(_ context.Context, fileID string) ([]byte, error) {
	f.calls++
	if b, ok := f.binaries[fileID]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("no download %s", fileID)
}

// parseSinceFromQuery extracts the RFC3339 modifiedTime gate from a fileQuery
// string so the fake can honor `Changes(since)`.
func parseSinceFromQuery(q string) time.Time {
	const marker = "modifiedTime > '"
	i := strings.Index(q, marker)
	if i < 0 {
		return time.Time{}
	}
	rest := q[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, rest[:j])
	return t
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func TestResolvePath(t *testing.T) {
	folders := map[string]folderInfo{
		"root":   {name: "root", parents: nil},
		"alpha":  {name: "Alpha", parents: []string{"root"}},
		"beta":   {name: "Beta", parents: []string{"alpha"}},
		"outer":  {name: "Outer", parents: nil},
	}
	f := driveFile{ID: "f1", Parents: []string{"beta"}}

	t.Run("whole drive resolves first-parent chain", func(t *testing.T) {
		seg, in := resolvePath(folders, f, "")
		if !in {
			t.Fatal("want inScope")
		}
		if want := []string{"root", "Alpha", "Beta"}; !equal(seg, want) {
			t.Fatalf("got %v want %v", seg, want)
		}
	})

	t.Run("rootID restricts and trims path", func(t *testing.T) {
		seg, in := resolvePath(folders, f, "alpha")
		if !in {
			t.Fatal("want inScope under alpha")
		}
		if want := []string{"Beta"}; !equal(seg, want) {
			t.Fatalf("got %v want %v", seg, want)
		}
	})

	t.Run("file outside root is out of scope", func(t *testing.T) {
		outsider := driveFile{ID: "f2", Parents: []string{"outer"}}
		if _, in := resolvePath(folders, outsider, "alpha"); in {
			t.Fatal("outsider must not be in scope")
		}
	})
}

func TestExtractContent_GoogleDocFallsBackToHTML(t *testing.T) {
	api := &fakeDrive{
		exports: map[string]map[string][]byte{
			"doc1": {
				// Native markdown export returns empty (Google's imperfect-export case).
				"text/markdown": []byte(""),
				"text/html":     []byte(`<h1>Hi</h1><p>body <strong>bold</strong></p>`),
			},
		},
	}
	got, ok, err := extractContent(context.Background(), api, driveFile{ID: "doc1", MimeType: mimeGDoc})
	if err != nil {
		t.Fatalf("extractContent: %v", err)
	}
	if !ok {
		t.Fatal("want ok")
	}
	if !strings.Contains(got, "# Hi") || !strings.Contains(got, "**bold**") {
		t.Fatalf("html fallback should produce markdown, got:\n%s", got)
	}
}

func TestExtractContent_UnsupportedSkipped(t *testing.T) {
	api := &fakeDrive{}
	_, ok, err := extractContent(context.Background(), api, driveFile{ID: "x", MimeType: "application/vnd.google-apps.form"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Fatal("forms should be skipped")
	}
}

func TestDocumentsAndChanges(t *testing.T) {
	t0 := mustTime(t, "2026-05-01T00:00:00Z")
	t1 := mustTime(t, "2026-05-15T00:00:00Z")
	api := &fakeDrive{
		folders: []driveFile{
			{ID: "rootf", Name: "Notes", MimeType: mimeFolder},
		},
		files: []driveFile{
			{ID: "g1", Name: "Plan", MimeType: mimeGDoc, Parents: []string{"rootf"}, ModifiedTime: t0},
			{ID: "g2", Name: "Updated", MimeType: mimeGDoc, Parents: []string{"rootf"}, ModifiedTime: t1},
			{ID: "skip", Name: "Form", MimeType: "application/vnd.google-apps.form", Parents: []string{"rootf"}, ModifiedTime: t1},
		},
		exports: map[string]map[string][]byte{
			"g1": {"text/markdown": []byte("# Plan\n\nbody")},
			"g2": {"text/markdown": []byte("# Updated\n\nlater")},
		},
	}
	c := newWithAPI("gd", "", api)
	c.cfg = connectors.ConnectorConfig{Name: "gd"}

	t.Run("Documents emits all supported in-scope files", func(t *testing.T) {
		docs, errs := c.Documents(context.Background(), connectors.StreamOpts{})
		got, err := connectors.Collect(docs, errs)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 docs (Plan, Updated), got %d", len(got))
		}
		for _, d := range got {
			if d.Source != "gd" || d.Hash == "" || len(d.Hierarchy) != 1 || d.Hierarchy[0] != "Notes" {
				t.Fatalf("unexpected doc shape: %+v", d)
			}
		}
	})

	t.Run("Changes honors since", func(t *testing.T) {
		ch, errs := c.Changes(context.Background(), t0.Add(time.Second))
		var got []core.Change
		for v := range ch {
			got = append(got, v)
		}
		if err := drain(errs); err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("want only the updated file, got %d", len(got))
		}
		if got[0].Document == nil || got[0].Document.Title != "Updated" {
			t.Fatalf("wrong change: %+v", got[0])
		}
	})

	t.Run("Enumerate matches Documents' ID set (deletion detection)", func(t *testing.T) {
		ids, errs := c.Enumerate(context.Background())
		var got []string
		for v := range ids {
			got = append(got, v)
		}
		if err := drain(errs); err != nil {
			t.Fatalf("err: %v", err)
		}
		// 2 supported, the form is skipped.
		if len(got) != 2 {
			t.Fatalf("want 2 enumerated IDs, got %d (%v)", len(got), got)
		}
	})
}

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func drain(errs <-chan error) error {
	for e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

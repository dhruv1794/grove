package local

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"grove/internal/connectors"
	"grove/internal/core"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func collectDocs(t *testing.T, c *Connector) []core.Document {
	t.Helper()
	out, err := connectors.Collect(c.Documents(context.Background(), connectors.StreamOpts{}))
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceRef < out[j].SourceRef })
	return out
}

func TestWalk_SkipsIgnoredDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.md"), "# Keep")
	writeFile(t, filepath.Join(root, ".git", "config.md"), "# Git")
	writeFile(t, filepath.Join(root, "node_modules", "pkg.md"), "# Pkg")

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDocs(t, c)
	if len(got) != 1 || got[0].SourceRef != "keep.md" {
		t.Errorf("got %d docs %+v, want only keep.md", len(got), got)
	}
}

func TestWalk_RespectsGitignore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "secret.md\ndrafts/\n")
	writeFile(t, filepath.Join(root, "keep.md"), "# Keep")
	writeFile(t, filepath.Join(root, "secret.md"), "# Secret")
	writeFile(t, filepath.Join(root, "drafts", "wip.md"), "# WIP")
	writeFile(t, filepath.Join(root, "notes", "ok.md"), "# Notes")

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDocs(t, c)
	gotRefs := make([]string, len(got))
	for i, d := range got {
		gotRefs[i] = d.SourceRef
	}
	want := []string{"keep.md", "notes/ok.md"}
	if len(gotRefs) != len(want) {
		t.Fatalf("got %v, want %v", gotRefs, want)
	}
	for i := range want {
		if gotRefs[i] != want[i] {
			t.Errorf("got %v, want %v", gotRefs, want)
		}
	}
}

func TestWalk_IncludeGlob(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.md"), "# A")
	writeFile(t, filepath.Join(root, "b.md"), "# B")
	writeFile(t, filepath.Join(root, "sub", "deep.md"), "# Deep")

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
		Include: []string{"sub/**"},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDocs(t, c)
	if len(got) != 1 || got[0].SourceRef != "sub/deep.md" {
		t.Errorf("include sub/**: got %+v, want only sub/deep.md", got)
	}
}

func TestWalk_ExcludeGlobWinsOverInclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.md"), "# A")
	writeFile(t, filepath.Join(root, "archive", "old.md"), "# Old")

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
		Include: []string{"**/*.md"},
		Exclude: []string{"archive/**"},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDocs(t, c)
	if len(got) != 1 || got[0].SourceRef != "a.md" {
		t.Errorf("exclude over include: got %+v, want only a.md", got)
	}
}

func TestConnect_RejectsInvalidGlob(t *testing.T) {
	c := New()
	err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": t.TempDir()},
		Exclude: []string{"[unclosed"},
	})
	if err == nil {
		t.Errorf("expected error for invalid glob, got nil")
	}
}

// makeTestPDF builds a minimal single-page PDF containing text, with an
// xref table whose offsets are computed so the file parses cleanly.
func makeTestPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 24 Tf 72 700 Td (%s) Tj ET", text)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /MediaBox [0 0 612 792] /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, body := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", len(objs)+1, xrefStart)
	return buf.Bytes()
}

func TestWalk_ExtractsPDFText(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "note.md"), "# Note")
	if err := os.WriteFile(filepath.Join(root, "doc.pdf"), makeTestPDF("Hello Grove"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDocs(t, c)
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (md + pdf)", len(got))
	}
	var pdfDoc *core.Document
	for i := range got {
		if got[i].SourceRef == "doc.pdf" {
			pdfDoc = &got[i]
		}
	}
	if pdfDoc == nil {
		t.Fatal("doc.pdf was not ingested")
	}
	if !strings.Contains(pdfDoc.Content, "Hello Grove") {
		t.Errorf("pdf content = %q, want it to contain %q", pdfDoc.Content, "Hello Grove")
	}
}

func TestWalk_SkipsUnreadablePDF(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "good.md"), "# Good")
	if err := os.WriteFile(filepath.Join(root, "broken.pdf"), []byte("not a pdf"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDocs(t, c)
	if len(got) != 1 || got[0].SourceRef != "good.md" {
		t.Errorf("got %+v, want only good.md (broken pdf skipped)", got)
	}
}

// makeTestDOCX builds a minimal .docx (a ZIP holding word/document.xml)
// with one <w:p> paragraph per supplied string.
func makeTestDOCX(t *testing.T, paragraphs ...string) []byte {
	t.Helper()
	var body strings.Builder
	body.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		body.WriteString(`<w:p><w:r><w:t>` + p + `</w:t></w:r></w:p>`)
	}
	body.WriteString(`</w:body></w:document>`)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body.String())); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWalk_ExtractsDOCXText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "memo.docx"),
		makeTestDOCX(t, "First paragraph.", "Second paragraph."), 0o644); err != nil {
		t.Fatal(err)
	}

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDocs(t, c)
	if len(got) != 1 || got[0].SourceRef != "memo.docx" {
		t.Fatalf("got %+v, want only memo.docx", got)
	}
	for _, want := range []string{"First paragraph.", "Second paragraph."} {
		if !strings.Contains(got[0].Content, want) {
			t.Errorf("docx content = %q, want it to contain %q", got[0].Content, want)
		}
	}
}

func TestWalk_SkipsUnreadableDOCX(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "good.md"), "# Good")
	if err := os.WriteFile(filepath.Join(root, "broken.docx"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDocs(t, c)
	if len(got) != 1 || got[0].SourceRef != "good.md" {
		t.Errorf("got %+v, want only good.md (broken docx skipped)", got)
	}
}

func TestWalk_ExtractsLinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "note.md"), strings.Join([]string{
		"See [[Other Note]] and [[Guide#Setup]] and [[Long Name|alias]].",
		"External [docs](https://example.com/page).",
		"Local [readme](../README.md#intro).",
		"An image ![logo](assets/logo.png) should be ignored.",
	}, "\n"))

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDocs(t, c)
	if len(got) != 1 {
		t.Fatalf("got %d docs, want 1", len(got))
	}
	want := []core.DocLink{
		{Type: core.LinkWiki, Target: "Other Note"},
		{Type: core.LinkWiki, Target: "Guide", Anchor: "Setup"},
		{Type: core.LinkWiki, Target: "Long Name"},
		{Type: core.LinkURL, Target: "https://example.com/page"},
		{Type: core.LinkDocRef, Target: "../README.md", Anchor: "intro"},
	}
	links := got[0].Links
	if len(links) != len(want) {
		t.Fatalf("got %d links %+v, want %d", len(links), links, len(want))
	}
	for i := range want {
		if links[i] != want[i] {
			t.Errorf("link[%d] = %+v, want %+v", i, links[i], want[i])
		}
	}
}

func TestWalk_ExtractsHierarchy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "top.md"), "# Top")
	writeFile(t, filepath.Join(root, "a", "mid.md"), "# Mid")
	writeFile(t, filepath.Join(root, "a", "b", "deep.md"), "# Deep")

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	byRef := make(map[string][]string)
	for _, d := range collectDocs(t, c) {
		byRef[d.SourceRef] = d.Hierarchy
	}
	if h := byRef["top.md"]; h != nil {
		t.Errorf("top.md hierarchy = %v, want nil (root file)", h)
	}
	if h := byRef["a/mid.md"]; len(h) != 1 || h[0] != "a" {
		t.Errorf("a/mid.md hierarchy = %v, want [a]", h)
	}
	if h := byRef["a/b/deep.md"]; len(h) != 2 || h[0] != "a" || h[1] != "b" {
		t.Errorf("a/b/deep.md hierarchy = %v, want [a b]", h)
	}
}

func TestChanges_FiltersByModTime(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "old.md")
	newPath := filepath.Join(root, "new.md")
	writeFile(t, oldPath, "# Old")
	writeFile(t, newPath, "# New")

	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	before := cutoff.Add(-time.Hour)
	after := cutoff.Add(time.Hour)
	if err := os.Chtimes(oldPath, before, before); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, after, after); err != nil {
		t.Fatal(err)
	}

	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	changes, errs := c.Changes(context.Background(), cutoff)
	var got []core.Change
	for ch := range changes {
		got = append(got, ch)
	}
	if err := <-errs; err != nil {
		t.Fatalf("changes error: %v", err)
	}
	if len(got) != 1 || got[0].Document.SourceRef != "new.md" {
		t.Errorf("got %d changes %+v, want only new.md", len(got), got)
	}
	if got[0].Type != core.ChangeModified {
		t.Errorf("change type = %q, want %q", got[0].Type, core.ChangeModified)
	}
}

func TestDocID_StableAndDistinct(t *testing.T) {
	a := docID("notes", "a/b.md")
	if a != docID("notes", "a/b.md") {
		t.Error("docID not deterministic for the same input")
	}
	if a == docID("notes", "a/c.md") {
		t.Error("docID collided across different refs")
	}
	if a == docID("other", "a/b.md") {
		t.Error("docID collided across different sources")
	}
	// IDs are 64-char hex (sha256).
	if len(a) != 64 {
		t.Errorf("docID length = %d, want 64", len(a))
	}
}

func TestSHA256Hex_Deterministic(t *testing.T) {
	h := sha256hex([]byte("hello world"))
	if h != sha256hex([]byte("hello world")) {
		t.Error("sha256hex not deterministic")
	}
	if h == sha256hex([]byte("hello worle")) {
		t.Error("sha256hex collided on different content")
	}
	// Content hash is independent of source_ref: the same bytes at two
	// different paths produce the same Hash (drives cache reuse on move).
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.md"), "identical body")
	writeFile(t, filepath.Join(root, "sub", "y.md"), "identical body")
	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDocs(t, c)
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2", len(got))
	}
	if got[0].Hash != got[1].Hash {
		t.Errorf("same content hashed differently: %q vs %q", got[0].Hash, got[1].Hash)
	}
	if got[0].ID == got[1].ID {
		t.Error("distinct source_refs produced the same doc ID")
	}
}

func TestWalk_NoGitignoreFileIsFine(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.md"), "# A")
	c := New()
	if err := c.Connect(context.Background(), connectors.ConnectorConfig{
		Name: "t", Custom: map[string]string{"path": root},
	}); err != nil {
		t.Fatal(err)
	}
	if got := collectDocs(t, c); len(got) != 1 {
		t.Errorf("got %d docs, want 1", len(got))
	}
}

package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"grove/internal/connectors"
	"grove/internal/core"
)

func TestSplitFrontmatter(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantBody string
		wantFM   string
	}{
		{"none", "# Title\nbody", "# Title\nbody", ""},
		{"basic", "---\ntags: [a, b]\n---\n# Title\nbody", "# Title\nbody", "tags: [a, b]"},
		{"crlf", "---\r\ntitle: X\r\n---\r\nbody", "body", "title: X"},
		{"empty-body", "---\ntags: [a]\n---\n", "", "tags: [a]"},
		{"not-at-start", "intro\n---\ntags: [a]\n---\n", "intro\n---\ntags: [a]\n---\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, fm := splitFrontmatter(tc.in)
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			if fm != tc.wantFM {
				t.Errorf("frontmatter = %q, want %q", fm, tc.wantFM)
			}
		})
	}
}

func TestFrontmatterTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"list", "tags: [concept, search]", []string{"concept", "search"}},
		{"block", "tags:\n  - a\n  - b", []string{"a", "b"}},
		{"scalar-spaces", "tags: a b c", []string{"a", "b", "c"}},
		{"scalar-commas", "tags: a, b", []string{"a", "b"}},
		{"singular-key", "tag: solo", []string{"solo"}},
		{"hash-prefixed", "tags: [\"#x\", y]", []string{"x", "y"}},
		{"none", "title: X", nil},
		{"malformed", "tags: [unclosed", nil},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := frontmatterTags(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInlineTags(t *testing.T) {
	body := "Notes #daily and #review/weekly here.\n# Heading not a tag\nhex #fff is, but #123 is not.\n(#parenthesized) and code#nope."
	got := inlineTags(body)
	sort.Strings(got)
	want := []string{"daily", "fff", "parenthesized", "review/weekly"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDecorate_StripsFrontmatterAndCollectsTags(t *testing.T) {
	doc := &core.Document{Content: "---\ntags: [concept, search]\n---\n# Retrieval\n\nBody with #inline tag.\n"}
	decorate(doc)

	if want := "# Retrieval\n\nBody with #inline tag.\n"; doc.Content != want {
		t.Errorf("content = %q, want %q", doc.Content, want)
	}
	want := []string{"concept", "inline", "search"} // deduped + sorted
	if !reflect.DeepEqual(doc.Metadata.Tags, want) {
		t.Errorf("tags = %v, want %v", doc.Metadata.Tags, want)
	}
}

// End-to-end: walking a vault yields markdown only, tags populated, frontmatter
// stripped, wikilinks extracted off the cleaned body, and .obsidian skipped.
func TestConnect_WalksVault(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ".obsidian", "app.json"), `{"attachmentFolderPath":"attachments"}`)
	mustWrite(t, filepath.Join(dir, "Concepts", "Retrieval.md"),
		"---\ntags: [concept, search]\n---\n# Retrieval\n\nSee [[Knowledge Graph]] and [[Retrieval#Ranking]].\n")
	mustWrite(t, filepath.Join(dir, "Daily", "2026-05-01.md"), "# Day\n\n#daily note\n")
	mustWrite(t, filepath.Join(dir, "image.png"), "not markdown")
	mustWrite(t, filepath.Join(dir, "attachments", "stray.md"), "should be skipped")

	conn := New()
	cfg := connectors.ConnectorConfig{Name: "vault", Custom: map[string]string{"path": dir}}
	if err := conn.Connect(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if !conn.Capabilities().SupportsRichMeta {
		t.Error("obsidian should report SupportsRichMeta")
	}

	docs, err := connectors.Collect(conn.Documents(context.Background(), connectors.StreamOpts{}))
	if err != nil {
		t.Fatal(err)
	}

	byRef := map[string]core.Document{}
	for _, d := range docs {
		byRef[filepath.ToSlash(d.SourceRef)] = d
	}
	if len(byRef) != 2 {
		t.Fatalf("indexed %d docs, want 2 (markdown only, attachments skipped): %v", len(byRef), keys(byRef))
	}

	r := byRef["Concepts/Retrieval.md"]
	if want := []string{"concept", "search"}; !reflect.DeepEqual(r.Metadata.Tags, want) {
		t.Errorf("Retrieval tags = %v, want %v", r.Metadata.Tags, want)
	}
	if got := r.Content; len(got) >= 3 && got[:3] == "---" {
		t.Errorf("frontmatter not stripped: %q", got)
	}
	var wikiTargets []string
	for _, l := range r.Links {
		if l.Type == core.LinkWiki {
			wikiTargets = append(wikiTargets, l.Target)
		}
	}
	sort.Strings(wikiTargets)
	if want := []string{"Knowledge Graph", "Retrieval"}; !reflect.DeepEqual(wikiTargets, want) {
		t.Errorf("wikilink targets = %v, want %v", wikiTargets, want)
	}

	if want := []string{"daily"}; !reflect.DeepEqual(byRef["Daily/2026-05-01.md"].Metadata.Tags, want) {
		t.Errorf("daily tags = %v, want %v", byRef["Daily/2026-05-01.md"].Metadata.Tags, want)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]core.Document) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

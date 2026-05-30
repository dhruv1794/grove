package confluence

import (
	"context"
	"strings"
	"testing"
	"time"

	"grove/internal/connectors"
)

type fakeAPI struct {
	spaces []confluenceSpace
	pages  map[string][]confluencePage // spaceID → metas
	bodies map[string]string           // pageID → storage xml
}

func (f *fakeAPI) listSpaces(_ context.Context) ([]confluenceSpace, error) {
	return f.spaces, nil
}

func (f *fakeAPI) listPageMeta(_ context.Context, spaceID string, since time.Time) ([]confluencePage, error) {
	out := []confluencePage{}
	for _, p := range f.pages[spaceID] {
		if !since.IsZero() && !p.Modified.After(since) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeAPI) getPageBody(_ context.Context, pageID string) (confluencePage, error) {
	for _, ps := range f.pages {
		for _, p := range ps {
			if p.ID == pageID {
				p.StorageXML = f.bodies[pageID]
				return p, nil
			}
		}
	}
	return confluencePage{}, nil
}

func TestStorageToMarkdown_BasicAndCodeMacro(t *testing.T) {
	in := `<h1>Title</h1>
<p>Intro <strong>bold</strong>.</p>
<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">go</ac:parameter><ac:plain-text-body><![CDATA[fmt.Println("hi")]]></ac:plain-text-body></ac:structured-macro>
<p>See <ac:link><ri:page ri:content-title="Other Page" /></ac:link> too.</p>`
	md, err := storageToMarkdown(in)
	if err != nil {
		t.Fatalf("storageToMarkdown: %v", err)
	}
	for _, want := range []string{
		"# Title",
		"**bold**",
		"```",
		`fmt.Println("hi")`,
		"[Other Page](Other Page)",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("want %q in:\n%s", want, md)
		}
	}
}

func TestResolveHierarchyAndDocumentsChanges(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	pages := []confluencePage{
		{ID: "p1", Title: "Root", SpaceID: "s1", Modified: t0},
		{ID: "p2", Title: "Child", SpaceID: "s1", ParentID: "p1", Modified: t0},
		{ID: "p3", Title: "Updated", SpaceID: "s1", ParentID: "p2", Modified: t1},
	}
	api := &fakeAPI{
		spaces: []confluenceSpace{{ID: "s1", Key: "ENG", Name: "Engineering"}},
		pages:  map[string][]confluencePage{"s1": pages},
		bodies: map[string]string{
			"p1": "<p>root body</p>",
			"p2": "<p>child body</p>",
			"p3": "<p>updated body</p>",
		},
	}
	c := newWithAPI("conf", "", api)
	c.cfg = connectors.ConnectorConfig{Name: "conf"}

	t.Run("Documents emits all pages with hierarchy", func(t *testing.T) {
		docs, errs := c.Documents(context.Background(), connectors.StreamOpts{})
		got, err := connectors.Collect(docs, errs)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("want 3 docs, got %d", len(got))
		}
		for _, d := range got {
			if d.Hierarchy[0] != "Engineering" {
				t.Fatalf("hierarchy must start with space name: %v", d.Hierarchy)
			}
			if d.Title == "Updated" {
				want := []string{"Engineering", "Root", "Child"}
				if !sliceEq(d.Hierarchy, want) {
					t.Fatalf("Updated hierarchy got %v want %v", d.Hierarchy, want)
				}
			}
		}
	})

	t.Run("Changes honors since", func(t *testing.T) {
		ch, errs := c.Changes(context.Background(), t0.Add(time.Second))
		count := 0
		var titles []string
		for v := range ch {
			count++
			titles = append(titles, v.Document.Title)
		}
		for range errs {
		}
		if count != 1 || titles[0] != "Updated" {
			t.Fatalf("want only Updated, got %v", titles)
		}
	})

	t.Run("Enumerate lists all page IDs", func(t *testing.T) {
		ids, errs := c.Enumerate(context.Background())
		n := 0
		for range ids {
			n++
		}
		for range errs {
		}
		if n != 3 {
			t.Fatalf("want 3 enumerated, got %d", n)
		}
	})
}

func sliceEq(a, b []string) bool {
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

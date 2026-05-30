package htmlmd

import (
	"strings"
	"testing"
)

func TestConvert(t *testing.T) {
	cases := []struct {
		name string
		html string
		want []string // substrings that must appear in order
	}{
		{
			name: "headings and paragraphs",
			html: `<h1>Title</h1><p>Hello <strong>world</strong> and <em>more</em>.</p>`,
			want: []string{"# Title", "Hello **world** and *more*."},
		},
		{
			name: "links and code",
			html: `<p>See <a href="https://x.com">site</a> and <code>fn()</code>.</p>`,
			want: []string{"[site](https://x.com)", "`fn()`"},
		},
		{
			name: "unordered list",
			html: `<ul><li>one</li><li>two</li></ul>`,
			want: []string{"- one", "- two"},
		},
		{
			name: "ordered list",
			html: `<ol><li>first</li><li>second</li></ol>`,
			want: []string{"1. first", "2. second"},
		},
		{
			name: "nested list",
			html: `<ul><li>parent<ul><li>child</li></ul></li></ul>`,
			want: []string{"- parent", "  - child"},
		},
		{
			name: "table",
			html: `<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>`,
			want: []string{"| A | B |", "| --- | --- |", "| 1 | 2 |"},
		},
		{
			name: "pre code block",
			html: "<pre>line1\nline2</pre>",
			want: []string{"```", "line1\nline2"},
		},
		{
			name: "heading levels",
			html: `<h3>Sub</h3>`,
			want: []string{"### Sub"},
		},
		{
			name: "unknown element keeps text",
			html: `<custom-thing>kept text</custom-thing>`,
			want: []string{"kept text"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Convert(tc.html)
			if err != nil {
				t.Fatalf("Convert: %v", err)
			}
			pos := 0
			for _, w := range tc.want {
				idx := strings.Index(got[pos:], w)
				if idx < 0 {
					t.Fatalf("want %q in order; got:\n%s", w, got)
				}
				pos += idx + len(w)
			}
		})
	}
}

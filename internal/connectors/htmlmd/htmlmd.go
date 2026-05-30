// Package htmlmd converts an HTML fragment to Markdown. It is intentionally
// focused on the tags the cloud connectors emit — Google Docs HTML export and
// Confluence storage format (XHTML) — not a general-purpose HTML renderer.
// Unknown elements degrade to rendering their children, so unrecognized markup
// loses formatting but never loses text.
package htmlmd

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Convert parses an HTML fragment and returns Markdown.
func Convert(htmlStr string) (string, error) {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}
	c := &converter{}
	c.block(findBody(doc))
	return strings.TrimSpace(collapseBlankLines(c.out.String())), nil
}

type converter struct {
	out      strings.Builder
	listType []byte // stack: 'u' (unordered) or a digit count for ordered
}

// block renders a node's children as block-level content, separating blocks
// with blank lines.
func (c *converter) block(n *html.Node) {
	if n == nil {
		return
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.renderBlock(ch)
	}
}

func (c *converter) renderBlock(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		if s := strings.TrimSpace(collapseSpaces(n.Data)); s != "" {
			c.writeLine(s)
		}
		return
	case html.ElementNode:
	default:
		return
	}

	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		level := int(n.DataAtom - atom.H1 + 1)
		c.writeLine(strings.Repeat("#", level) + " " + c.inline(n))
	case atom.P:
		if s := c.inline(n); s != "" {
			c.writeLine(s)
		}
	case atom.Pre:
		c.writeCodeBlock(textContent(n))
	case atom.Blockquote:
		var sub converter
		sub.block(n)
		for line := range strings.SplitSeq(strings.TrimSpace(sub.out.String()), "\n") {
			c.writeLine("> " + line)
		}
	case atom.Ul:
		c.list(n, false)
	case atom.Ol:
		c.list(n, true)
	case atom.Hr:
		c.writeLine("---")
	case atom.Table:
		c.table(n)
	case atom.Br:
		// standalone <br> between blocks: ignore
	default:
		// div, section, article, body, span used as block, etc.: recurse.
		c.block(n)
	}
}

func (c *converter) list(n *html.Node, ordered bool) {
	i := 0
	for li := n.FirstChild; li != nil; li = li.NextSibling {
		if li.Type != html.ElementNode || li.DataAtom != atom.Li {
			continue
		}
		i++
		indent := strings.Repeat("  ", len(c.listType))
		marker := "- "
		if ordered {
			marker = fmt.Sprintf("%d. ", i)
		}
		// Inline content of the <li>, excluding nested lists.
		text := c.inlineExcludingLists(li)
		c.writeLine(indent + marker + text)

		// Nested lists render indented beneath the item.
		for sub := li.FirstChild; sub != nil; sub = sub.NextSibling {
			if sub.Type == html.ElementNode && (sub.DataAtom == atom.Ul || sub.DataAtom == atom.Ol) {
				c.listType = append(c.listType, 'x')
				c.list(sub, sub.DataAtom == atom.Ol)
				c.listType = c.listType[:len(c.listType)-1]
			}
		}
	}
}

func (c *converter) table(n *html.Node) {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		for ch := nd.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.ElementNode && ch.DataAtom == atom.Tr {
				var cells []string
				for cell := ch.FirstChild; cell != nil; cell = cell.NextSibling {
					if cell.Type == html.ElementNode && (cell.DataAtom == atom.Td || cell.DataAtom == atom.Th) {
						cells = append(cells, strings.TrimSpace(c.inline(cell)))
					}
				}
				if len(cells) > 0 {
					rows = append(rows, cells)
				}
			} else if ch.Type == html.ElementNode {
				walk(ch)
			}
		}
	}
	walk(n)
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	pad := func(r []string) []string {
		for len(r) < cols {
			r = append(r, "")
		}
		return r
	}
	c.writeLine("| " + strings.Join(pad(rows[0]), " | ") + " |")
	c.writeLine("| " + strings.Join(repeat("---", cols), " | ") + " |")
	for _, r := range rows[1:] {
		c.writeLine("| " + strings.Join(pad(r), " | ") + " |")
	}
}

// inline renders a node's children as inline Markdown (no block separation).
func (c *converter) inline(n *html.Node) string {
	return c.inlineFiltered(n, false)
}

func (c *converter) inlineExcludingLists(n *html.Node) string {
	return c.inlineFiltered(n, true)
}

func (c *converter) inlineFiltered(n *html.Node, skipLists bool) string {
	var b strings.Builder
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		b.WriteString(c.inlineNode(ch, skipLists))
	}
	return strings.TrimSpace(collapseSpaces(b.String()))
}

func (c *converter) inlineNode(n *html.Node, skipLists bool) string {
	switch n.Type {
	case html.TextNode:
		return collapseSpaces(n.Data)
	case html.ElementNode:
	default:
		return ""
	}
	switch n.DataAtom {
	case atom.Strong, atom.B:
		return "**" + c.inlineFiltered(n, skipLists) + "**"
	case atom.Em, atom.I:
		return "*" + c.inlineFiltered(n, skipLists) + "*"
	case atom.Code:
		return "`" + textContent(n) + "`"
	case atom.A:
		text := c.inlineFiltered(n, skipLists)
		href := attr(n, "href")
		if href == "" {
			return text
		}
		if text == "" {
			text = href
		}
		return fmt.Sprintf("[%s](%s)", text, href)
	case atom.Img:
		alt := attr(n, "alt")
		src := attr(n, "src")
		if src == "" {
			return ""
		}
		return fmt.Sprintf("![%s](%s)", alt, src)
	case atom.Br:
		return "\n"
	case atom.Ul, atom.Ol:
		if skipLists {
			return ""
		}
		return c.inlineFiltered(n, skipLists)
	default:
		return c.inlineFiltered(n, skipLists)
	}
}

func (c *converter) writeLine(s string) {
	c.out.WriteString(s)
	c.out.WriteString("\n\n")
}

func (c *converter) writeCodeBlock(code string) {
	c.out.WriteString("```\n")
	c.out.WriteString(strings.TrimRight(code, "\n"))
	c.out.WriteString("\n```\n\n")
}

func findBody(doc *html.Node) *html.Node {
	var body *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if body != nil {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Body {
			body = n
			return
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	if body != nil {
		return body
	}
	return doc
}

// textContent returns the concatenated text of a node and its descendants,
// preserving inner whitespace (used for <pre>/<code>).
func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if nd.Type == html.TextNode {
			b.WriteString(nd.Data)
		}
		for ch := nd.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	return b.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

var spaceReplacer = strings.NewReplacer("\n", " ", "\t", " ", "\r", " ", " ", " ")

// collapseSpaces normalizes runs of whitespace (incl. newlines and &nbsp;) to a
// single space, matching HTML's inline-whitespace handling.
func collapseSpaces(s string) string {
	s = spaceReplacer.Replace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

// collapseBlankLines collapses 3+ consecutive newlines to exactly two.
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

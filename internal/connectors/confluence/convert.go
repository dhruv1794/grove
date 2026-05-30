package confluence

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"grove/internal/connectors/htmlmd"
	"grove/internal/core"
)

// storageToMarkdown converts an Atlassian storage-format (XHTML) blob to
// Markdown. The format is mostly XHTML with a sprinkling of Confluence-specific
// elements (ac:structured-macro for code/info/etc, ri:page links). We
// preprocess the high-value macros into standard HTML so htmlmd can render
// them; anything left over degrades to its children's text, which preserves
// the body even if formatting is lost.
func storageToMarkdown(storage string) (string, error) {
	return htmlmd.Convert(preprocessStorage(storage))
}

var (
	// <ac:structured-macro ac:name="code">…<ac:plain-text-body><![CDATA[X]]></ac:plain-text-body>…</ac:structured-macro>
	codeMacroRe = regexp.MustCompile(`(?s)<ac:structured-macro[^>]*ac:name="code"[^>]*>.*?<ac:plain-text-body><!\[CDATA\[(.*?)\]\]></ac:plain-text-body>.*?</ac:structured-macro>`)
	// <ac:link><ri:page ri:content-title="X" /></ac:link>  (self- or paired-close)
	pageLinkRe = regexp.MustCompile(`(?s)<ac:link[^>]*>\s*<ri:page[^>]*ri:content-title="([^"]+)"[^>]*/?>\s*</ac:link>`)
	// <ac:image><ri:attachment ri:filename="X"/></ac:image>
	attachImgRe = regexp.MustCompile(`(?s)<ac:image[^>]*>\s*<ri:attachment[^>]*ri:filename="([^"]+)"[^>]*/?>\s*</ac:image>`)
	// info/note/warning panels — keep the body, drop the macro wrapper.
	panelMacroRe = regexp.MustCompile(`(?s)<ac:structured-macro[^>]*ac:name="(info|note|warning|tip)"[^>]*>(.*?)</ac:structured-macro>`)
)

func preprocessStorage(s string) string {
	s = codeMacroRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := codeMacroRe.FindStringSubmatch(m)
		body := sub[1]
		return "<pre><code>" + escapeHTML(body) + "</code></pre>"
	})
	s = pageLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := pageLinkRe.FindStringSubmatch(m)
		title := sub[1]
		return `<a href="` + title + `">` + title + `</a>`
	})
	s = attachImgRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := attachImgRe.FindStringSubmatch(m)
		return `<img alt="` + sub[1] + `" src="` + sub[1] + `"/>`
	})
	s = panelMacroRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := panelMacroRe.FindStringSubmatch(m)
		body := sub[2]
		// Drop a leading rich-text-body wrapper so the panel's text flows
		// inline; the panel's "kind" (info/note/etc) is lost in v0.3.
		body = strings.ReplaceAll(body, "<ac:rich-text-body>", "")
		body = strings.ReplaceAll(body, "</ac:rich-text-body>", "")
		return body
	})
	return s
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// resolveHierarchy walks the page-tree parent chain (newest-to-root), returning
// the path as space-name → ancestor titles → (page itself excluded). Cycles are
// bounded by maxDepth.
func resolveHierarchy(pages map[string]confluencePage, spaceName string, p confluencePage) []string {
	const maxDepth = 64
	var rev []string
	cur := p.ParentID
	for depth := 0; cur != "" && depth < maxDepth; depth++ {
		anc, ok := pages[cur]
		if !ok {
			break
		}
		rev = append(rev, anc.Title)
		cur = anc.ParentID
	}
	out := []string{spaceName}
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out
}

// buildDocument turns a fetched page (with body) into a core.Document. Pages is
// the full page-index for hierarchy resolution.
func buildDocument(source, collection, spaceName string, pages map[string]confluencePage, p confluencePage) (core.Document, error) {
	md, err := storageToMarkdown(p.StorageXML)
	if err != nil {
		return core.Document{}, err
	}
	hierarchy := resolveHierarchy(pages, spaceName, p)
	meta := core.DocMetadata{
		Modified: p.Modified,
		Custom: map[string]string{
			"page_id":  p.ID,
			"space_id": p.SpaceID,
		},
	}
	if p.WebURL != "" {
		meta.Custom["web_url"] = p.WebURL
	}
	return core.Document{
		ID:         docID(source, p.ID),
		Source:     source,
		SourceRef:  p.ID,
		Collection: collection,
		Title:      p.Title,
		Content:    md,
		Metadata:   meta,
		Hierarchy:  hierarchy,
		Hash:       sha256hex([]byte(md)),
	}, nil
}

func docID(source, ref string) string {
	h := sha256.Sum256([]byte(source + "\x00" + ref))
	return hex.EncodeToString(h[:])
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

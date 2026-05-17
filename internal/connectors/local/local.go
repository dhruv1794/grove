package local

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"grove/internal/connectors"
	"grove/internal/core"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/dslipak/pdf"
	gitignore "github.com/sabhiram/go-gitignore"
)

// supportedExt lists file extensions the local connector ingests in v0.1.
var supportedExt = map[string]bool{
	".md":   true,
	".txt":  true,
	".pdf":  true,
	".docx": true,
}

// ignoredDirs are always skipped during the walk, independent of .gitignore.
var ignoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"__pycache__":  true,
	".venv":        true,
	".grove":       true,
}

type Connector struct {
	name       string
	root       string
	cfg        connectors.ConnectorConfig
	gitignore  *gitignore.GitIgnore // nil if root has no .gitignore
	hasFilters bool                 // true if gitignore or include/exclude globs are set
}

func New() *Connector { return &Connector{} }

func (c *Connector) Name() string { return c.name }

func (c *Connector) Connect(ctx context.Context, cfg connectors.ConnectorConfig) error {
	path := cfg.Custom["path"]
	if path == "" {
		return fmt.Errorf("local connector requires a path")
	}
	abs, err := filepath.Abs(expandHome(path))
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("path %q: %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path %q is not a directory", abs)
	}
	c.root = abs
	c.cfg = cfg
	c.name = cfg.Name

	// Load root .gitignore if present. Nested .gitignores are not honored
	// in v0.1 — most repos have one at the root and that covers 95% of cases.
	if gi, err := gitignore.CompileIgnoreFile(filepath.Join(abs, ".gitignore")); err == nil {
		c.gitignore = gi
	}

	// Validate glob patterns up front so bad input fails at Connect time,
	// not silently mid-walk.
	for _, pat := range append(cfg.Include, cfg.Exclude...) {
		if !doublestar.ValidatePattern(pat) {
			return fmt.Errorf("invalid glob pattern %q", pat)
		}
	}

	c.hasFilters = c.gitignore != nil || len(cfg.Include) > 0 || len(cfg.Exclude) > 0
	return nil
}

// pathAllowed reports whether the path relative to the root passes the
// configured --include / --exclude filters. Exclude wins over include.
// If no include patterns are set, all unmatched-by-exclude files pass.
func (c *Connector) pathAllowed(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, pat := range c.cfg.Exclude {
		if ok, _ := doublestar.Match(pat, rel); ok {
			return false
		}
	}
	if len(c.cfg.Include) == 0 {
		return true
	}
	for _, pat := range c.cfg.Include {
		if ok, _ := doublestar.Match(pat, rel); ok {
			return true
		}
	}
	return false
}

func (c *Connector) Disconnect(ctx context.Context) error { return nil }

func (c *Connector) Validate(ctx context.Context) error {
	if c.root == "" {
		return fmt.Errorf("not connected")
	}
	_, err := os.Stat(c.root)
	return err
}

func (c *Connector) Capabilities() connectors.Capabilities {
	return connectors.Capabilities{
		SupportsIncremental: true,
		SupportsLinks:       true,
		SupportsRichMeta:    false,
		SupportsAuth:        connectors.AuthNone,
		NativeFormat:        "markdown",
	}
}

// dirFiltered reports whether a directory should be skipped because the
// root .gitignore matches it. Glob filters apply to files, not directories.
func (c *Connector) dirFiltered(path string) bool {
	if c.gitignore == nil {
		return false
	}
	rel, err := filepath.Rel(c.root, path)
	if err != nil || rel == "." {
		return false
	}
	return c.gitignore.MatchesPath(rel + "/")
}

// fileFiltered reports whether a file is excluded by .gitignore or the
// configured --include / --exclude globs. Short-circuits when no filters
// are configured so the common case skips the filepath.Rel cost.
func (c *Connector) fileFiltered(path string) bool {
	if !c.hasFilters {
		return false
	}
	rel, err := filepath.Rel(c.root, path)
	if err != nil {
		return false
	}
	if c.gitignore != nil && c.gitignore.MatchesPath(rel) {
		return true
	}
	return !c.pathAllowed(rel)
}

// walkDocs walks the root and invokes emit for each supported file.
// The optional accept predicate skips files (e.g. mtime filter for Changes).
func (c *Connector) walkDocs(ctx context.Context, accept func(fs.FileInfo) bool, emit func(core.Document) error) error {
	return filepath.WalkDir(c.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if ignoredDirs[d.Name()] || c.dirFiltered(path) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !supportedExt[ext] {
			return nil
		}
		if c.fileFiltered(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if c.cfg.MaxSizeMB > 0 && info.Size() > c.cfg.MaxSizeMB*1024*1024 {
			return nil
		}
		if accept != nil && !accept(info) {
			return nil
		}
		doc, err := c.readDoc(path, ext, info)
		if err != nil {
			// A malformed PDF/DOCX shouldn't abort indexing the rest of
			// the corpus; surface it and skip. Filesystem errors on text
			// files are real problems and still propagate.
			switch ext {
			case ".pdf", ".docx":
				fmt.Fprintf(os.Stderr, "grove: skipping unreadable file %s: %v\n", path, err)
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return emit(doc)
	})
}

func (c *Connector) Documents(ctx context.Context, opts connectors.StreamOpts) (<-chan core.Document, <-chan error) {
	docs := make(chan core.Document, 128)
	errs := make(chan error, 1)
	go func() {
		defer close(docs)
		defer close(errs)
		err := c.walkDocs(ctx, nil, func(doc core.Document) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case docs <- doc:
				return nil
			}
		})
		if err != nil {
			errs <- err
		}
	}()
	return docs, errs
}

func (c *Connector) Changes(ctx context.Context, since time.Time) (<-chan core.Change, <-chan error) {
	changes := make(chan core.Change, 128)
	errs := make(chan error, 1)
	go func() {
		defer close(changes)
		defer close(errs)
		accept := func(info fs.FileInfo) bool { return info.ModTime().After(since) }
		err := c.walkDocs(ctx, accept, func(doc core.Document) error {
			ch := core.Change{DocID: doc.ID, Type: core.ChangeModified, Document: &doc}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case changes <- ch:
				return nil
			}
		})
		if err != nil {
			errs <- err
		}
	}()
	return changes, errs
}

func (c *Connector) readDoc(path, ext string, info fs.FileInfo) (core.Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return core.Document{}, err
	}
	// The hash addresses the on-disk file (for change detection); Content
	// holds the ingestible text — for PDF/DOCX that's the extracted text.
	var content string
	switch ext {
	case ".pdf":
		content, err = extractPDFText(raw)
	case ".docx":
		content, err = extractDOCXText(raw)
	default:
		content = string(raw)
	}
	if err != nil {
		return core.Document{}, err
	}
	rel, err := filepath.Rel(c.root, path)
	if err != nil {
		rel = path
	}
	hierarchy := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
	if len(hierarchy) == 1 && hierarchy[0] == "." {
		hierarchy = nil
	}
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	hash := sha256hex(raw)
	id := docID(c.name, rel)
	return core.Document{
		ID:         id,
		Source:     c.name,
		SourceRef:  rel,
		Collection: c.cfg.Collection,
		Title:      title,
		Content:    content,
		Metadata: core.DocMetadata{
			Modified:  info.ModTime(),
			SizeBytes: info.Size(),
		},
		Links:     extractLinks(content),
		Hierarchy: hierarchy,
		Hash:      hash,
	}, nil
}

// extractPDFText pulls plain text from a PDF's content streams. It handles
// digitally-generated PDFs only — scanned/image PDFs yield no text and OCR
// is out of scope for v0.1. The underlying parser can panic on malformed
// input, so a recover converts that into a normal error.
func extractPDFText(raw []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pdf parse panic: %v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", err
	}
	rd, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	if _, err := io.Copy(&buf, rd); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// wordprocessingNS is the OOXML WordprocessingML namespace; .docx text
// elements live under it.
const wordprocessingNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

// extractDOCXText pulls plain text from a .docx file. A .docx is a ZIP
// archive whose body text lives in word/document.xml as <w:t> runs;
// paragraphs (<w:p>) become newlines. Styling, tables, and images are
// dropped — v0.1 indexes text only.
func extractDOCXText(raw []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", err
	}
	var body *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			body = f
			break
		}
	}
	if body == nil {
		return "", fmt.Errorf("not a docx: missing word/document.xml")
	}
	rc, err := body.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	var buf strings.Builder
	var inText bool
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != wordprocessingNS {
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				buf.WriteByte('\t')
			case "br", "cr":
				buf.WriteByte('\n')
			}
		case xml.EndElement:
			if t.Name.Space != wordprocessingNS {
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				buf.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				buf.Write(t)
			}
		}
	}
	return strings.TrimSpace(buf.String()), nil
}

var (
	wikiLinkRe  = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)
	mdLinkRe    = regexp.MustCompile(`(!?)\[[^\]]*\]\(([^)\s]+)\)`)
	urlSchemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)
)

// extractLinks pulls outbound references out of document text: wikilinks
// (`[[Note]]`, `[[Note#Heading]]`, `[[Note|Alias]]`) and markdown links
// (`[text](target)`). Targets are kept raw — resolving them to doc IDs
// needs the whole corpus and so happens at build time, not in the connector.
func extractLinks(content string) []core.DocLink {
	var links []core.DocLink
	for _, m := range wikiLinkRe.FindAllStringSubmatch(content, -1) {
		target, _, _ := strings.Cut(m[1], "|") // [[Note|Alias]] → Note
		target, anchor, _ := strings.Cut(target, "#")
		if target = strings.TrimSpace(target); target != "" {
			links = append(links, core.DocLink{Type: core.LinkWiki, Target: target, Anchor: anchor})
		}
	}
	for _, m := range mdLinkRe.FindAllStringSubmatch(content, -1) {
		if m[1] == "!" { // image, not a link
			continue
		}
		if urlSchemeRe.MatchString(m[2]) {
			links = append(links, core.DocLink{Type: core.LinkURL, Target: m[2]})
			continue
		}
		target, anchor, _ := strings.Cut(m[2], "#")
		if target != "" {
			links = append(links, core.DocLink{Type: core.LinkDocRef, Target: target, Anchor: anchor})
		}
	}
	return links
}

func docID(source, ref string) string {
	h := sha256.Sum256([]byte(source + "\x00" + ref))
	return hex.EncodeToString(h[:])
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

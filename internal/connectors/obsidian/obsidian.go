package obsidian

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"grove/internal/connectors"
	"grove/internal/connectors/local"
	"grove/internal/core"

	"gopkg.in/yaml.v3"
)

// markdownExt is the set obsidian indexes — markdown only. Attachments, PDFs,
// and the like are deliberately skipped (an Obsidian vault is markdown notes).
var markdownExt = map[string]bool{
	".md":       true,
	".markdown": true,
}

// VaultConfig holds the subset of `.obsidian/` settings grove reads. Everything
// is best-effort: a vault with no config (or unreadable config) still indexes.
type VaultConfig struct {
	AttachmentFolder string // app.json "attachmentFolderPath"
	DailyNotesFolder string // daily-notes.json "folder"
}

// Connector indexes an Obsidian vault. It composes the local connector
// (filesystem walk, change detection, link extraction) and layers vault-aware
// behavior on top: markdown-only ingest, frontmatter/inline tags, and skipping
// the `.obsidian/` config directory.
type Connector struct {
	*local.Connector
	vault VaultConfig
}

func New() *Connector { return &Connector{Connector: local.New()} }

func (c *Connector) Connect(ctx context.Context, cfg connectors.ConnectorConfig) error {
	path := cfg.Custom["path"]
	if path == "" {
		return fmt.Errorf("obsidian connector requires a path")
	}
	c.vault = readVaultConfig(path)

	c.Connector.RestrictExtensions(markdownExt)
	// `.obsidian` is vault config; `.trash` is Obsidian's soft-delete folder.
	c.Connector.IgnoreDirs(".obsidian", ".trash")
	if folder := strings.TrimSpace(c.vault.AttachmentFolder); folder != "" && !strings.ContainsAny(folder, "/\\") {
		// A bare attachment-folder name (not a path) is a top-level dir we skip;
		// markdown-only ingest already drops most attachments, this covers any
		// stray notes filed there.
		c.Connector.IgnoreDirs(folder)
	}
	c.Connector.SetDecorator(decorate)

	return c.Connector.Connect(ctx, cfg)
}

func (c *Connector) Capabilities() connectors.Capabilities {
	caps := c.Connector.Capabilities()
	caps.SupportsRichMeta = true // tags from frontmatter + inline #tags
	return caps
}

// VaultConfig returns the parsed `.obsidian/` settings (zero value if none).
func (c *Connector) VaultConfig() VaultConfig { return c.vault }

// decorate strips YAML frontmatter from the note body and lifts tags into
// Metadata.Tags. It runs before the local connector re-extracts links, so
// wikilinks are read off the cleaned body.
func decorate(doc *core.Document) {
	body, fm := splitFrontmatter(doc.Content)
	doc.Content = body

	tagSet := map[string]bool{}
	for _, t := range frontmatterTags(fm) {
		tagSet[t] = true
	}
	for _, t := range inlineTags(body) {
		tagSet[t] = true
	}
	if len(tagSet) == 0 {
		return
	}
	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	doc.Metadata.Tags = tags
}

var frontmatterRe = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n?`)

// splitFrontmatter separates a leading YAML frontmatter block from the body.
// Returns (body, rawFrontmatterYAML); rawFrontmatter is "" when none is present.
func splitFrontmatter(content string) (body, frontmatter string) {
	m := frontmatterRe.FindStringSubmatchIndex(content)
	if m == nil {
		return content, ""
	}
	frontmatter = content[m[2]:m[3]]
	body = content[m[1]:]
	return body, frontmatter
}

// frontmatterTags parses the `tags` (or `tag`) key from frontmatter YAML.
// Obsidian accepts a YAML list, or a space/comma-separated scalar.
func frontmatterTags(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return nil // malformed frontmatter: skip tags, keep the note
	}
	val, ok := doc["tags"]
	if !ok {
		val = doc["tag"]
	}
	var out []string
	switch v := val.(type) {
	case string:
		out = append(out, splitScalarTags(v)...)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				if s = normalizeTag(s); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// splitScalarTags handles `tags: a b, c` style scalars.
func splitScalarTags(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if t := normalizeTag(f); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// inlineTagRe matches Obsidian inline tags: `#tag`, `#nested/tag`. Tags must
// contain at least one non-numeric character (so `#123` and a markdown heading
// like `# Heading` — which has a space after `#` — are not tags). A preceding
// boundary keeps it from matching inside words or URL fragments.
var inlineTagRe = regexp.MustCompile(`(?:^|[\s(])#([A-Za-z0-9_/-]*[A-Za-z_][A-Za-z0-9_/-]*)`)

func inlineTags(body string) []string {
	var out []string
	for _, m := range inlineTagRe.FindAllStringSubmatch(body, -1) {
		if t := normalizeTag(m[1]); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// normalizeTag trims a leading `#` and surrounding space; tags are stored
// without the `#`.
func normalizeTag(s string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
}

// readVaultConfig loads the subset of `.obsidian/` settings grove uses. Missing
// or malformed files yield a zero value — the vault still indexes as a folder.
func readVaultConfig(vaultPath string) VaultConfig {
	var vc VaultConfig
	dir := filepath.Join(vaultPath, ".obsidian")

	if b, err := os.ReadFile(filepath.Join(dir, "app.json")); err == nil {
		var app struct {
			AttachmentFolderPath string `json:"attachmentFolderPath"`
		}
		if err := json.Unmarshal(b, &app); err == nil {
			vc.AttachmentFolder = app.AttachmentFolderPath
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "daily-notes.json")); err == nil {
		var daily struct {
			Folder string `json:"folder"`
		}
		if err := json.Unmarshal(b, &daily); err == nil {
			vc.DailyNotesFolder = daily.Folder
		}
	}
	return vc
}

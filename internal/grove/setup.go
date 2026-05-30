package grove

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"grove/internal/core"
)

// SetupStatus reports whether each required field for a source type is set in
// the resolved (merged + env) config.
type SetupStatus struct {
	Type   string
	Path   string // config file path the user should edit
	Fields []SetupField
}

type SetupField struct {
	Key     string // toml key, e.g. "client_id"
	Set     bool
	Hint    string // remediation when unset
	Secret  bool   // if true, never print the value
	Display string // value to show when set (empty for secrets)
}

// SetupSource ensures the user's global config has the [<type>] section
// pre-populated with empty keys (or leaves an existing section untouched),
// then reports per-field status. The CLI/REPL adapter opens the returned path
// in $EDITOR so the user can fill in client_id / client_secret / etc.
//
// Global config is chosen so the credentials persist across every workspace.
func (g *Grove) SetupSource(srcType string) (*SetupStatus, error) {
	path, err := core.GlobalConfigPath()
	if err != nil {
		return nil, err
	}
	if err := ensureSourceSection(path, srcType); err != nil {
		return nil, err
	}
	cfg, err := core.LoadMergedConfig(g.configPath)
	if err != nil {
		return nil, err
	}
	return &SetupStatus{Type: srcType, Path: path, Fields: setupFields(srcType, cfg)}, nil
}

func setupFields(srcType string, cfg core.Config) []SetupField {
	switch srcType {
	case "gdrive":
		return []SetupField{
			{Key: "client_id", Set: cfg.Gdrive.ClientID != "", Display: cfg.Gdrive.ClientID,
				Hint: "Google Cloud → OAuth client (Desktop) → copy Client ID"},
			{Key: "client_secret", Set: cfg.Gdrive.ClientSecret != "", Secret: true,
				Hint: "copy Client Secret from the same OAuth client modal"},
			{Key: "default_folder", Set: cfg.Gdrive.DefaultFolder != "", Display: cfg.Gdrive.DefaultFolder,
				Hint: "the Drive folder ID to ingest (URL: …/folders/<this>); optional — can pass --folder per connect"},
		}
	case "confluence":
		return []SetupField{
			{Key: "client_id", Set: cfg.Confluence.ClientID != "", Display: cfg.Confluence.ClientID,
				Hint: "developer.atlassian.com → OAuth 2.0 (3LO) app → Settings → Client ID"},
			{Key: "client_secret", Set: cfg.Confluence.ClientSecret != "", Secret: true,
				Hint: "Settings → Secret from the same Atlassian app"},
		}
	}
	return nil
}

// ensureSourceSection appends a stub [<type>] section to the file at path if
// one isn't there yet. The template includes setup links so a user opening the
// file in an editor for the first time has everything they need inline.
// Preserves existing content verbatim (we don't round-trip through the TOML
// encoder so hand-written comments survive).
func ensureSourceSection(path, srcType string) error {
	tmpl := sectionTemplate(srcType)
	if tmpl == "" {
		return fmt.Errorf("unknown source type %q (use gdrive or confluence)", srcType)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(existing)
	section := "[" + srcType + "]"
	if strings.Contains(content, section) {
		return nil
	}
	sep := ""
	if content != "" && !strings.HasSuffix(content, "\n\n") {
		if strings.HasSuffix(content, "\n") {
			sep = "\n"
		} else {
			sep = "\n\n"
		}
	}
	return os.WriteFile(path, []byte(content+sep+tmpl), 0o600)
}

func sectionTemplate(srcType string) string {
	switch srcType {
	case "gdrive":
		return `# Google Drive
# 1. Create a Google Cloud project: https://console.cloud.google.com/
# 2. Enable: Google Drive API + Google Docs API.
# 3. OAuth consent screen → External, Testing, add your Gmail as a test user.
# 4. Credentials → Create OAuth client → Desktop app → copy ID + secret.
[gdrive]
client_id = ""
client_secret = ""
# Optional: default Drive folder ID (the part after /folders/ in the URL).
default_folder = ""
`
	case "confluence":
		return `# Confluence Cloud
# 1. Register an OAuth 2.0 (3LO) app: https://developer.atlassian.com/console/myapps/
# 2. Permissions → Confluence API → add scopes:
#      read:confluence-content.all
#      read:confluence-space.summary
# 3. Authorization → add OAuth 2.0 (3LO) → Callback URL (exactly):
#      http://127.0.0.1:53682/callback
# 4. Settings → copy Client ID and Secret.
[confluence]
client_id = ""
client_secret = ""
`
	}
	return ""
}

package core

import (
	"fmt"
	"strings"
)

// RenderConfigTemplate produces a fully-annotated config.toml seeded with c's
// values. Empty fields render as commented-out examples so a user opening the
// file sees the available knobs + the env-only vars they can't put here. Both
// `grove init` (workspace creation) and `grove config init` use this — the
// template is human-friendly TOML, not the toml-encoder round-trip, so the
// comments survive.
func RenderConfigTemplate(c Config) string {
	var b strings.Builder
	b.WriteString("# grove configuration\n")
	b.WriteString("# Safe to edit. Layering: ~/.grove/config.toml (global) ⊕\n")
	b.WriteString("# this file (local, if a workspace) ⊕ environment variables.\n")
	b.WriteString("# Env wins over both files; local wins over global.\n\n")

	sv := c.Workspace.SchemaVersion
	if sv == 0 {
		sv = 1
	}
	fmt.Fprintf(&b, "[workspace]\nschema_version = %d\n\n", sv)

	b.WriteString("# ───── Models ─────\n")
	b.WriteString("# Format: \"provider/name\". Empty = pass --model on the command line.\n")
	b.WriteString("# Examples: ollama/qwen2.5:14b · deepseek/deepseek-chat · anthropic/claude-sonnet-4-6\n\n")

	b.WriteString("[build]\n")
	emitModel(&b, c.Build.Model, "used by `grove build`")
	b.WriteString("\n[query]\n")
	emitModel(&b, c.Query.Model, "used by `grove ask`")
	b.WriteString("\n")

	b.WriteString("# ───── Cloud connectors ─────\n")
	b.WriteString("# Env vars (GROVE_*_CLIENT_ID / _CLIENT_SECRET) override these.\n")
	b.WriteString("# Secrets here are PLAIN TEXT — chmod 600 the file.\n\n")

	b.WriteString("# Google Drive — setup:\n")
	b.WriteString("#   1. console.cloud.google.com → new project, enable Drive + Docs APIs\n")
	b.WriteString("#   2. OAuth consent screen → External, Testing, add your Gmail as test user\n")
	b.WriteString("#   3. Credentials → OAuth client → Desktop app → copy ID + secret here\n")
	b.WriteString("[gdrive]\n")
	emitField(&b, "client_id", c.Gdrive.ClientID, false)
	emitField(&b, "client_secret", c.Gdrive.ClientSecret, false)
	b.WriteString("# Optional default Drive folder ID (the part after /folders/ in the URL):\n")
	emitField(&b, "default_folder", c.Gdrive.DefaultFolder, false)
	b.WriteString("\n")

	b.WriteString("# Confluence Cloud — OAuth 2.0 (3LO) — setup:\n")
	b.WriteString("#   1. developer.atlassian.com/console/myapps/ → OAuth 2.0 (3LO)\n")
	b.WriteString("#   2. Permissions → Confluence API → scopes:\n")
	b.WriteString("#        read:confluence-content.all, read:confluence-space.summary\n")
	b.WriteString("#   3. Authorization → Callback URL (exactly):\n")
	b.WriteString("#        http://127.0.0.1:53682/callback\n")
	b.WriteString("[confluence]\n")
	emitField(&b, "client_id", c.Confluence.ClientID, false)
	emitField(&b, "client_secret", c.Confluence.ClientSecret, false)
	b.WriteString("\n")

	b.WriteString("# ───── Environment-only settings ─────\n")
	b.WriteString("# These are NOT in this file by design (API keys / per-shell).\n")
	b.WriteString("# Set them in your shell rc:\n")
	b.WriteString("#\n")
	b.WriteString("#   GROVE_DEEPSEEK_API_KEY=sk-…         DeepSeek API key (deepseek/* models)\n")
	b.WriteString("#   GROVE_OPENAI_API_KEY=sk-…           OpenAI API key\n")
	b.WriteString("#   GROVE_ANTHROPIC_API_KEY=sk-ant-…    Anthropic API key\n")
	b.WriteString("#   GROVE_OLLAMA_HOST=http://host:11434 Override the default Ollama URL\n")
	b.WriteString("#   GROVE_EMBED_MODEL=ollama/bge-m3     Override the embed model\n")
	b.WriteString("#   GROVE_BUILD_MODEL=…                 One-shot override of [build].model\n")
	b.WriteString("#   GROVE_QUERY_MODEL=…                 One-shot override of [query].model\n")
	b.WriteString("#   GROVE_WORKSPACE=/path/to/workspace  Default workspace path\n")
	return b.String()
}

// emitModel writes a [build]/[query] model line — set value or commented stub.
func emitModel(b *strings.Builder, val, comment string) {
	fmt.Fprintf(b, "# %s\n", comment)
	if val == "" {
		fmt.Fprintf(b, "# model = \"\"\n")
		return
	}
	fmt.Fprintf(b, "model = %q\n", val)
}

// emitField writes one key = "value" line — uncommented if val is set, with
// an empty stub otherwise so the user can see the available knob.
func emitField(b *strings.Builder, key, val string, _ bool) {
	fmt.Fprintf(b, "%s = %q\n", key, val)
}

package core

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) Layout {
	t.Helper()
	// Neutralize model env vars so file-parsing assertions stay deterministic
	// regardless of the test runner's environment.
	t.Setenv("GROVE_BUILD_MODEL", "")
	t.Setenv("GROVE_QUERY_MODEL", "")
	l := NewLayout(t.TempDir())
	if err := os.WriteFile(l.ConfigTOML, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLoadConfig_ParsesModels(t *testing.T) {
	l := writeConfig(t, `
[workspace]
schema_version = 1

[build]
model = "ollama/qwen2.5:32b"

[query]
model = "anthropic/claude-sonnet-4-6"
`)
	cfg, err := LoadConfig(l)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Workspace.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", cfg.Workspace.SchemaVersion)
	}
	if cfg.Build.Model != "ollama/qwen2.5:32b" {
		t.Errorf("build model = %q", cfg.Build.Model)
	}
	if cfg.Query.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("query model = %q", cfg.Query.Model)
	}
}

func TestLoadConfig_OmittedModelsAreEmpty(t *testing.T) {
	l := writeConfig(t, `
[workspace]
schema_version = 1

[build]
# model = "ollama/qwen2.5:32b"

[query]
`)
	cfg, err := LoadConfig(l)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Build.Model != "" || cfg.Query.Model != "" {
		t.Errorf("expected empty models, got build=%q query=%q", cfg.Build.Model, cfg.Query.Model)
	}
}

func TestLoadConfig_EnvOverridesFileModel(t *testing.T) {
	l := writeConfig(t, `
[build]
model = "ollama/qwen2.5:32b"

[query]
model = "anthropic/claude-sonnet-4-6"
`)
	t.Setenv("GROVE_BUILD_MODEL", "openai/gpt-4o-mini")
	t.Setenv("GROVE_QUERY_MODEL", "ollama/llama3.1:70b")
	cfg, err := LoadConfig(l)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Build.Model != "openai/gpt-4o-mini" {
		t.Errorf("build model = %q, want env override", cfg.Build.Model)
	}
	if cfg.Query.Model != "ollama/llama3.1:70b" {
		t.Errorf("query model = %q, want env override", cfg.Query.Model)
	}
}

func TestLoadConfig_EnvFillsWhenFileOmits(t *testing.T) {
	l := writeConfig(t, "[build]\n[query]\n")
	t.Setenv("GROVE_BUILD_MODEL", "openai/gpt-4o-mini")
	cfg, err := LoadConfig(l)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Build.Model != "openai/gpt-4o-mini" {
		t.Errorf("build model = %q, want env value", cfg.Build.Model)
	}
	if cfg.Query.Model != "" {
		t.Errorf("query model = %q, want empty (no env, no file)", cfg.Query.Model)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	l := NewLayout(filepath.Join(t.TempDir(), "nonexistent"))
	if _, err := LoadConfig(l); err == nil {
		t.Error("expected error for missing config.toml, got nil")
	}
}

func TestLoadConfig_InvalidTOML(t *testing.T) {
	l := writeConfig(t, "[build\nmodel = ")
	if _, err := LoadConfig(l); err == nil {
		t.Error("expected error for malformed TOML, got nil")
	}
}

// LoadConfigFile reads a config at an arbitrary path — the capability the
// CLI's --config flag depends on (a file outside the workspace).
func TestLoadConfigFile_ExplicitPath(t *testing.T) {
	t.Setenv("GROVE_BUILD_MODEL", "")
	t.Setenv("GROVE_QUERY_MODEL", "")
	path := filepath.Join(t.TempDir(), "custom-config.toml")
	if err := os.WriteFile(path, []byte("[build]\nmodel = \"ollama/qwen2.5:32b\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.Build.Model != "ollama/qwen2.5:32b" {
		t.Errorf("build model = %q, want ollama/qwen2.5:32b", cfg.Build.Model)
	}
}

// setHome points HOME at a temp dir so GlobalConfigPath resolves under it,
// and returns the global config path within it.
func setHome(t *testing.T) string {
	t.Helper()
	t.Setenv("GROVE_BUILD_MODEL", "")
	t.Setenv("GROVE_QUERY_MODEL", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	gp, err := GlobalConfigPath()
	if err != nil {
		t.Fatalf("GlobalConfigPath: %v", err)
	}
	if filepath.Dir(filepath.Dir(gp)) != home {
		t.Fatalf("global path %q not under HOME %q", gp, home)
	}
	if err := os.MkdirAll(filepath.Dir(gp), 0o755); err != nil {
		t.Fatal(err)
	}
	return gp
}

func TestLoadMergedConfig_GlobalUsedWhenNoLocal(t *testing.T) {
	gp := setHome(t)
	if err := os.WriteFile(gp, []byte("[query]\nmodel = \"ollama/global-q\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadMergedConfig(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("LoadMergedConfig: %v", err)
	}
	if cfg.Query.Model != "ollama/global-q" {
		t.Errorf("query model = %q, want ollama/global-q", cfg.Query.Model)
	}
}

func TestLoadMergedConfig_LocalOverridesGlobalPerKey(t *testing.T) {
	gp := setHome(t)
	// Global sets both models; local overrides only query → build falls through.
	if err := os.WriteFile(gp, []byte("[build]\nmodel=\"ollama/global-b\"\n[query]\nmodel=\"ollama/global-q\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(local, []byte("[query]\nmodel=\"ollama/local-q\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadMergedConfig(local)
	if err != nil {
		t.Fatalf("LoadMergedConfig: %v", err)
	}
	if cfg.Query.Model != "ollama/local-q" {
		t.Errorf("query model = %q, want local override ollama/local-q", cfg.Query.Model)
	}
	if cfg.Build.Model != "ollama/global-b" {
		t.Errorf("build model = %q, want global fall-through ollama/global-b", cfg.Build.Model)
	}
}

func TestLoadMergedConfig_EnvBeatsBoth(t *testing.T) {
	gp := setHome(t)
	if err := os.WriteFile(gp, []byte("[query]\nmodel=\"ollama/global-q\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(local, []byte("[query]\nmodel=\"ollama/local-q\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_QUERY_MODEL", "ollama/env-q")
	cfg, err := LoadMergedConfig(local)
	if err != nil {
		t.Fatalf("LoadMergedConfig: %v", err)
	}
	if cfg.Query.Model != "ollama/env-q" {
		t.Errorf("query model = %q, want env override ollama/env-q", cfg.Query.Model)
	}
}

func TestUpdateConfigFile_RoundTripPreservesOtherKeys(t *testing.T) {
	setHome(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[build]\nmodel=\"ollama/keep-b\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateConfigFile(path, func(c *Config) { c.Query.Model = "ollama/new-q" }); err != nil {
		t.Fatalf("UpdateConfigFile: %v", err)
	}
	cfg, err := LoadRawConfig(path)
	if err != nil {
		t.Fatalf("LoadRawConfig: %v", err)
	}
	if cfg.Query.Model != "ollama/new-q" {
		t.Errorf("query model = %q, want ollama/new-q", cfg.Query.Model)
	}
	if cfg.Build.Model != "ollama/keep-b" {
		t.Errorf("build model = %q, want preserved ollama/keep-b", cfg.Build.Model)
	}
}

func TestUpdateConfigFile_CreatesMissingFileAndDir(t *testing.T) {
	setHome(t)
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := UpdateConfigFile(path, func(c *Config) { c.Query.Model = "ollama/x" }); err != nil {
		t.Fatalf("UpdateConfigFile: %v", err)
	}
	if cfg, err := LoadRawConfig(path); err != nil || cfg.Query.Model != "ollama/x" {
		t.Fatalf("readback: cfg=%+v err=%v", cfg, err)
	}
}

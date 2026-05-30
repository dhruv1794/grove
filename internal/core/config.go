package core

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the parsed contents of a workspace's config.toml. Missing keys
// decode to zero values — an empty Model means "no default, --model required".
type Config struct {
	Workspace  WorkspaceConfig  `toml:"workspace" json:"workspace"`
	Build      ModelConfig      `toml:"build" json:"build"`
	Query      ModelConfig      `toml:"query" json:"query"`
	Gdrive     GDriveConfig     `toml:"gdrive" json:"gdrive"`
	Confluence ConfluenceConfig `toml:"confluence" json:"confluence"`
}

type WorkspaceConfig struct {
	SchemaVersion int `toml:"schema_version" json:"schema_version"`
}

type ModelConfig struct {
	Model string `toml:"model" json:"model"`
}

// GDriveConfig holds Google Drive connector credentials + defaults. Env vars
// (GROVE_GDRIVE_CLIENT_ID / GROVE_GDRIVE_CLIENT_SECRET) override the file.
type GDriveConfig struct {
	ClientID      string `toml:"client_id" json:"client_id"`
	ClientSecret  string `toml:"client_secret" json:"client_secret"`
	DefaultFolder string `toml:"default_folder" json:"default_folder"`
}

// ConfluenceConfig holds Atlassian OAuth credentials. Env vars
// (GROVE_CONFLUENCE_CLIENT_ID / GROVE_CONFLUENCE_CLIENT_SECRET) override.
type ConfluenceConfig struct {
	ClientID     string `toml:"client_id" json:"client_id"`
	ClientSecret string `toml:"client_secret" json:"client_secret"`
}

// LoadConfig reads the workspace's default config.toml. See LoadConfigFile.
func LoadConfig(l Layout) (Config, error) {
	return LoadConfigFile(l.ConfigTOML)
}

// LoadConfigFile reads a config.toml at an explicit path and applies
// environment overrides. Precedence: environment variable > config file >
// zero value. The --model flag on `build`/`ask` takes precedence over all of
// these. Missing file is an error — see LoadMergedConfig / LoadRawConfig for
// the tolerant variants.
func LoadConfigFile(path string) (Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	applyEnvOverrides(&cfg)
	return cfg, nil
}

// LoadRawConfig decodes a single config file with no environment overrides. A
// missing file yields a zero Config (not an error) — callers read absence as
// "unset". Used to test whether a file actually defines a key.
func LoadRawConfig(path string) (Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	return cfg, nil
}

// LoadMergedConfig resolves the effective config for a workspace: the global
// config (~/.grove/config.toml) overlaid by the local file at localPath, then
// environment overrides. Precedence low→high: global < local < env (a --model
// flag still wins above all). The overlay is per-key — TOML decode only sets
// keys present in a file, so a key absent from the local file falls through to
// the global value. Missing files are skipped; a malformed one errors.
func LoadMergedConfig(localPath string) (Config, error) {
	var cfg Config
	if gp, err := GlobalConfigPath(); err == nil {
		if err := decodeIfExists(gp, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := decodeIfExists(localPath, &cfg); err != nil {
		return Config{}, err
	}
	applyEnvOverrides(&cfg)
	return cfg, nil
}

// decodeIfExists overlays a config file onto cfg, skipping a missing file.
func decodeIfExists(path string, cfg *Config) error {
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	return nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("GROVE_BUILD_MODEL"); v != "" {
		cfg.Build.Model = v
	}
	if v := os.Getenv("GROVE_QUERY_MODEL"); v != "" {
		cfg.Query.Model = v
	}
	if v := os.Getenv("GROVE_GDRIVE_CLIENT_ID"); v != "" {
		cfg.Gdrive.ClientID = v
	}
	if v := os.Getenv("GROVE_GDRIVE_CLIENT_SECRET"); v != "" {
		cfg.Gdrive.ClientSecret = v
	}
	if v := os.Getenv("GROVE_CONFLUENCE_CLIENT_ID"); v != "" {
		cfg.Confluence.ClientID = v
	}
	if v := os.Getenv("GROVE_CONFLUENCE_CLIENT_SECRET"); v != "" {
		cfg.Confluence.ClientSecret = v
	}
}

// GlobalConfigPath returns ~/.grove/config.toml — the cross-workspace default
// config. GROVE_WORKSPACE intentionally does not affect it; it is global.
func GlobalConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".grove", "config.toml"), nil
}

// UpdateConfigFile applies mutate to the config at path and writes it back,
// preserving unrelated settings. It reads the raw file (no env overrides) so an
// environment-only value isn't baked into the file; a missing file starts from
// zero. Comments/formatting are not preserved (the encoder rewrites the file).
func UpdateConfigFile(path string, mutate func(*Config)) error {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	mutate(&cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir for %s: %w", path, err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return fmt.Errorf("encode config %s: %w", path, err)
	}
	return nil
}

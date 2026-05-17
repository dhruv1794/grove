package core

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is the parsed contents of a workspace's config.toml. Missing keys
// decode to zero values — an empty Model means "no default, --model required".
type Config struct {
	Workspace WorkspaceConfig `toml:"workspace" json:"workspace"`
	Build     ModelConfig     `toml:"build" json:"build"`
	Query     ModelConfig     `toml:"query" json:"query"`
}

type WorkspaceConfig struct {
	SchemaVersion int `toml:"schema_version" json:"schema_version"`
}

type ModelConfig struct {
	Model string `toml:"model" json:"model"`
}

// LoadConfig reads the workspace's default config.toml. See LoadConfigFile.
func LoadConfig(l Layout) (Config, error) {
	return LoadConfigFile(l.ConfigTOML)
}

// LoadConfigFile reads a config.toml at an explicit path and applies
// environment overrides. Precedence: environment variable > config file >
// zero value. (A future --model flag on `build`/`ask` will take precedence
// over all of these.)
func LoadConfigFile(path string) (Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if v := os.Getenv("GROVE_BUILD_MODEL"); v != "" {
		cfg.Build.Model = v
	}
	if v := os.Getenv("GROVE_QUERY_MODEL"); v != "" {
		cfg.Query.Model = v
	}
	return cfg, nil
}

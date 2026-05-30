package grove

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"grove/internal/connectors"
	"grove/internal/connectors/confluence"
	"grove/internal/connectors/gdrive"
	"grove/internal/connectors/local"
	"grove/internal/connectors/obsidian"
	"grove/internal/core"
	syncpkg "grove/internal/sync"
)

// SyncOpts configures Sync.
type SyncOpts struct {
	Model  string // build model for the incremental rebuild; flag > env > config
	Source string // sync only this source; "" syncs every source
	Force  bool   // rebuild the forest even if no documents changed, ignoring the node cache

	// OnProgress, if set, is forwarded to the rebuild for per-source progress.
	OnProgress func(BuildProgress)
}

// SourceSync reports the document-level changes detected for one source.
type SourceSync struct {
	Source   string `json:"source"`
	Created  int    `json:"created"`
	Modified int    `json:"modified"`
	Deleted  int    `json:"deleted"`
}

// SyncResult reports a completed sync run. Build is nil when nothing changed
// (and --force was not set), so a no-op sync makes no model call.
type SyncResult struct {
	Sources []SourceSync `json:"sources"`
	Build   *BuildResult `json:"build,omitempty"`
}

// Changed reports whether any source had document changes.
func (r *SyncResult) Changed() bool {
	for _, s := range r.Sources {
		if s.Created+s.Modified+s.Deleted > 0 {
			return true
		}
	}
	return false
}

// Sync re-walks connected sources, applies document changes (creates, content
// changes, deletions) to the store, and rebuilds the forest incrementally. The
// rebuild reuses Build, whose content-hash node cache regenerates only the
// branches whose documents changed.
func (g *Grove) Sync(ctx context.Context, opts SyncOpts) (*SyncResult, error) {
	sources, err := g.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	if opts.Source != "" {
		filtered := sources[:0]
		for _, s := range sources {
			if s.Name == opts.Source {
				filtered = append(filtered, s)
			}
		}
		sources = filtered
		if len(sources) == 0 {
			return nil, core.NewError(core.KindNoSources,
				fmt.Sprintf("no source named %q", opts.Source),
				"run `grove sources` to list connected sources")
		}
	}
	if len(sources) == 0 {
		return nil, core.NewError(core.KindNoSources,
			"no sources connected",
			"connect one with `grove connect local <path>`")
	}

	res := &SyncResult{}
	for _, src := range sources {
		conn, err := g.connectorFor(src)
		if err != nil {
			return nil, err
		}
		// --force re-hashes every file (zero since); otherwise only files
		// touched since the last sync are read.
		since := src.LastSyncAt
		if opts.Force {
			since = time.Time{}
		}
		diff, err := syncpkg.Compute(ctx, g.store, conn, src.Name, since)
		if err != nil {
			return nil, fmt.Errorf("sync %s: %w", src.Name, err)
		}
		if err := syncpkg.Apply(ctx, g.store, diff); err != nil {
			return nil, fmt.Errorf("sync %s: %w", src.Name, err)
		}
		if err := g.store.TouchSourceSync(ctx, src.Name, time.Now()); err != nil {
			return nil, err
		}
		res.Sources = append(res.Sources, SourceSync{
			Source:   src.Name,
			Created:  len(diff.Created),
			Modified: len(diff.Modified),
			Deleted:  len(diff.Deleted),
		})
	}

	if res.Changed() || opts.Force {
		build, err := g.Build(ctx, BuildOpts{
			Model:      opts.Model,
			Source:     opts.Source,
			Rebuild:    opts.Force,
			OnProgress: opts.OnProgress,
		})
		if err != nil {
			return nil, err
		}
		res.Build = build
	}
	return res, nil
}

// SourceRoots returns the filesystem root for each filesystem-backed source
// (local, obsidian), keyed by source name — the directories `grove sync
// --watch` watches. Sources with no path are omitted. opts source filter: ""
// means every source.
func (g *Grove) SourceRoots(ctx context.Context, source string) (map[string]string, error) {
	sources, err := g.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, src := range sources {
		if source != "" && src.Name != source {
			continue
		}
		cfg, err := parseSourceConfig(src)
		if err != nil {
			return nil, err
		}
		if p := cfg.Custom["path"]; p != "" {
			out[src.Name] = p
		}
	}
	return out, nil
}

// parseSourceConfig decodes a stored source's connector config.
func parseSourceConfig(src core.Source) (connectors.ConnectorConfig, error) {
	var cfg connectors.ConnectorConfig
	if err := json.Unmarshal([]byte(src.ConfigJSON), &cfg); err != nil {
		return cfg, fmt.Errorf("source %q: parse stored config: %w", src.Name, err)
	}
	return cfg, nil
}

// connectorFor reconstructs a source's connector from its stored type and
// config so sync can re-walk it. This is the seam where the grove adapter knows
// the concrete connector types; the sync core stays connector-agnostic. Cloud
// connectors get the live workspace auth directory injected here (never a
// persisted absolute path) so their stored tokens are found after a workspace
// move.
func (g *Grove) connectorFor(src core.Source) (connectors.Connector, error) {
	cfg, err := parseSourceConfig(src)
	if err != nil {
		return nil, err
	}
	var conn connectors.Connector
	switch src.Type {
	case core.SourceLocal:
		conn = local.New()
	case core.SourceObsidian:
		conn = obsidian.New()
	case core.SourceGDrive:
		conn = gdrive.New()
		if cfg.Custom == nil {
			cfg.Custom = map[string]string{}
		}
		cfg.Custom["auth_dir"] = g.layout.Auth
		gcfg, _ := core.LoadMergedConfig(g.configPath)
		cfg.Custom["client_id"] = gcfg.Gdrive.ClientID
		cfg.Custom["client_secret"] = gcfg.Gdrive.ClientSecret
	case core.SourceConfluence:
		conn = confluence.New()
		if cfg.Custom == nil {
			cfg.Custom = map[string]string{}
		}
		cfg.Custom["auth_dir"] = g.layout.Auth
		gcfg, _ := core.LoadMergedConfig(g.configPath)
		cfg.Custom["client_id"] = gcfg.Confluence.ClientID
		cfg.Custom["client_secret"] = gcfg.Confluence.ClientSecret
	default:
		return nil, core.NewError(core.KindMisuse,
			fmt.Sprintf("source %q has unsupported type %q for sync", src.Name, src.Type),
			"this grove build cannot sync that source type")
	}
	if err := conn.Connect(context.Background(), cfg); err != nil {
		return nil, fmt.Errorf("source %q: reconnect: %w", src.Name, err)
	}
	return conn, nil
}

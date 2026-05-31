package grove

import (
	"context"

	"grove/internal/compress"
	"grove/internal/core"
	"grove/internal/indexer"
	"grove/internal/llm"
)

// BuildOpts configures Build.
type BuildOpts struct {
	Model       string // "provider/name"; if empty, falls back to config [build].model
	Source      string // build only this source; "" builds every source
	Rebuild     bool
	DryRun      bool
	NoGroup     bool // skip LLM topic grouping of flat folders
	Concurrency int  // max in-flight model calls; <= 1 builds sequentially
	// Compress sets the document-compression level (none|safe|aggressive);
	// resolved flag > GROVE_BUILD_COMPRESS > [build].compress. "" means none.
	Compress string

	// OnProgress, if set, receives per-source node-build progress for an
	// adapter to render. Nil disables progress reporting.
	OnProgress func(BuildProgress)
}

// BuildResult reports a completed build run.
type BuildResult = indexer.Result

// BuildProgress reports per-source node-build progress.
type BuildProgress = indexer.Progress

// Build indexes connected sources into the knowledge forest. The build model
// is resolved with precedence flag > GROVE_BUILD_MODEL > config [build].model.
func (g *Grove) Build(ctx context.Context, opts BuildOpts) (*BuildResult, error) {
	// LoadMergedConfig overlays local on global and applies env overrides
	// (GROVE_BUILD_MODEL, GROVE_BUILD_COMPRESS). Used for both model and
	// compression resolution; a load error leaves the config zero-valued.
	cfg, _ := core.LoadMergedConfig(g.configPath)
	model := opts.Model
	if model == "" {
		model = cfg.Build.Model
	}
	if model == "" {
		return nil, core.NewError(core.KindMisuse,
			"no build model specified",
			"pass --model provider/name (e.g. ollama/llama3.1:8b), or set [build].model in config.toml")
	}
	compressStr := opts.Compress
	if compressStr == "" {
		compressStr = cfg.Build.Compress
	}
	level, err := compress.ParseLevel(compressStr)
	if err != nil {
		return nil, err
	}
	spec, err := llm.ParseModel(model)
	if err != nil {
		return nil, err
	}
	client, err := llm.New(spec, llm.OptionsFromEnv())
	if err != nil {
		return nil, err
	}
	return indexer.Build(ctx, indexer.Deps{
		Store:  g.store,
		LLM:    client,
		Layout: g.layout,
	}, indexer.Options{
		Source:      opts.Source,
		Rebuild:     opts.Rebuild,
		DryRun:      opts.DryRun,
		NoGroup:     opts.NoGroup,
		Concurrency: opts.Concurrency,
		Compress:    level,
		OnProgress:  opts.OnProgress,
	})
}

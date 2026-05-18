package grove

import (
	"context"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/internal/query"
)

// AskOpts configures Ask.
type AskOpts struct {
	Query        string
	Model        string // "provider/name"; if empty, falls back to config [query].model
	Source       string
	MaxDepth     int
	MaxTokens    int
	RetrieveOnly bool
	Fast         bool // keyword/FTS retrieval, no LLM tree descent
}

// AskResult is a completed query.
type AskResult = query.Result

// Ask answers a question over the knowledge forest. The query model is
// resolved with precedence flag > GROVE_QUERY_MODEL > config [query].model.
//
// An LLM is needed for tree descent and for answer synthesis; --fast skips
// descent, and --retrieve-only skips synthesis, so `--fast --retrieve-only`
// resolves no model and makes no network call.
func (g *Grove) Ask(ctx context.Context, opts AskOpts) (*AskResult, error) {
	if opts.Query == "" {
		return nil, core.NewError(core.KindMisuse,
			"empty query",
			"pass a question, e.g. grove ask \"what's our auth flow?\"")
	}
	var client llm.LLM
	needModel := !(opts.Fast && opts.RetrieveOnly)
	if needModel {
		model := opts.Model
		if model == "" {
			if cfg, err := core.LoadConfigFile(g.configPath); err == nil {
				model = cfg.Query.Model
			}
		}
		if model == "" {
			return nil, core.NewError(core.KindMisuse,
				"no query model specified",
				"pass --model provider/name (e.g. ollama/llama3.1:8b), or set [query].model in config.toml")
		}
		spec, err := llm.ParseModel(model)
		if err != nil {
			return nil, err
		}
		if client, err = llm.New(spec, llm.OptionsFromEnv()); err != nil {
			return nil, err
		}
	}
	return query.Ask(ctx, query.Deps{Store: g.store, LLM: client}, opts.Query, query.Options{
		Source:       opts.Source,
		MaxDepth:     opts.MaxDepth,
		MaxTokens:    opts.MaxTokens,
		RetrieveOnly: opts.RetrieveOnly,
		Fast:         opts.Fast,
	})
}

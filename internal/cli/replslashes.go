package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"grove/internal/grove"
)

// matchSlash reports whether line starts with /<name> (also accepts the legacy
// :name form) and returns the remaining whitespace-split tokens.
func matchSlash(line, name string) (rest []string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil, false
	}
	if c := "/" + strings.TrimLeft(fields[0], "/:"); c != "/"+name {
		return nil, false
	}
	return fields[1:], true
}

// ─── /sync ──────────────────────────────────────────────────────────────

type syncArgs struct {
	Source string
	Force  bool
}

func isSyncCmd(line string) (syncArgs, bool, error) {
	rest, ok := matchSlash(line, "sync")
	if !ok {
		return syncArgs{}, false, nil
	}
	var a syncArgs
	for i := 0; i < len(rest); i++ {
		switch tok := rest[i]; {
		case tok == "--force":
			a.Force = true
		case strings.HasPrefix(tok, "--"):
			return a, true, fmt.Errorf("unknown flag %q", tok)
		default:
			if a.Source == "" {
				a.Source = tok
			}
		}
	}
	return a, true, nil
}

func runSync(ctx context.Context, g *grove.Grove, a syncArgs, onProgress func(grove.BuildProgress)) (*grove.SyncResult, error) {
	return g.Sync(ctx, grove.SyncOpts{Source: a.Source, Force: a.Force, OnProgress: onProgress})
}

func renderSyncResult(out io.Writer, r *grove.SyncResult) {
	for _, s := range r.Sources {
		fmt.Fprintf(out, "%s: %d new, %d changed, %d deleted\n", s.Source, s.Created, s.Modified, s.Deleted)
	}
	if r.Build == nil {
		fmt.Fprintln(out, "up to date — no rebuild needed")
		return
	}
	b := r.Build
	fmt.Fprintf(out, "rebuilt %d trees, %d nodes (%d from cache, %d generated)\n",
		b.Trees, b.Nodes, b.CacheHits, b.CacheMiss)
}

// ─── /embed ─────────────────────────────────────────────────────────────

type embedArgs struct {
	Source string
	Model  string
	Chunks bool
}

func isEmbedCmd(line string) (embedArgs, bool, error) {
	rest, ok := matchSlash(line, "embed")
	if !ok {
		return embedArgs{}, false, nil
	}
	var a embedArgs
	for i := 0; i < len(rest); i++ {
		switch tok := rest[i]; {
		case tok == "--source" && i+1 < len(rest):
			a.Source = rest[i+1]
			i++
		case tok == "--model" && i+1 < len(rest):
			a.Model = rest[i+1]
			i++
		case tok == "--chunks":
			a.Chunks = true
		case strings.HasPrefix(tok, "--"):
			return a, true, fmt.Errorf("unknown flag %q", tok)
		default:
			return a, true, fmt.Errorf("unexpected arg %q", tok)
		}
	}
	return a, true, nil
}

func runEmbed(ctx context.Context, g *grove.Grove, a embedArgs, onProgress func(grove.EmbedProgress)) (*grove.EmbedResult, error) {
	return g.Embed(ctx, grove.EmbedOpts{Source: a.Source, Model: a.Model, Chunks: a.Chunks, OnProgress: onProgress})
}

func renderEmbedResult(out io.Writer, r *grove.EmbedResult) {
	fmt.Fprintf(out, "embedded %d docs with %s\n", r.Embedded, r.Model)
	if r.NodesEmbedded > 0 {
		fmt.Fprintf(out, "embedded %d node summaries\n", r.NodesEmbedded)
	}
	if r.ChunksEmbedded > 0 {
		fmt.Fprintf(out, "embedded %d passage chunks\n", r.ChunksEmbedded)
	}
	if r.Skipped > 0 {
		fmt.Fprintf(out, "skipped %d doc(s) too long for the embedder\n", r.Skipped)
	}
}

// ─── /tree ──────────────────────────────────────────────────────────────

type treeArgs struct {
	Ref   string
	Depth int
}

func isTreeCmd(line string) (treeArgs, bool, error) {
	rest, ok := matchSlash(line, "tree")
	if !ok {
		return treeArgs{}, false, nil
	}
	if len(rest) == 0 {
		return treeArgs{}, true, fmt.Errorf("usage: /tree <doc-id|path> [--depth N]")
	}
	var a treeArgs
	for i := 0; i < len(rest); i++ {
		switch tok := rest[i]; {
		case tok == "--depth" && i+1 < len(rest):
			var n int
			if _, err := fmt.Sscanf(rest[i+1], "%d", &n); err != nil || n < 0 {
				return a, true, fmt.Errorf("invalid --depth %q", rest[i+1])
			}
			a.Depth = n
			i++
		case strings.HasPrefix(tok, "--"):
			return a, true, fmt.Errorf("unknown flag %q", tok)
		default:
			if a.Ref == "" {
				a.Ref = tok
			}
		}
	}
	if a.Ref == "" {
		return a, true, fmt.Errorf("usage: /tree <doc-id|path> [--depth N]")
	}
	return a, true, nil
}

// ─── /status ────────────────────────────────────────────────────────────

func isStatusCmd(line string) bool {
	_, ok := matchSlash(line, "status")
	return ok
}

func renderStatusBrief(out io.Writer, r *grove.StatusReport) {
	fmt.Fprintf(out, "workspace: %s\n", r.Workspace)
	fmt.Fprintf(out, "sources: %d\n", len(r.Sources))
	for _, s := range r.Sources {
		fmt.Fprintf(out, "  %s [%s] · %d docs\n", s.Name, s.Type, s.DocCount)
	}
}

// ─── /disconnect ────────────────────────────────────────────────────────

func isDisconnectCmd(line string) (string, bool, error) {
	rest, ok := matchSlash(line, "disconnect")
	if !ok {
		return "", false, nil
	}
	if len(rest) == 0 {
		return "", true, fmt.Errorf("usage: /disconnect <source>")
	}
	return rest[0], true, nil
}

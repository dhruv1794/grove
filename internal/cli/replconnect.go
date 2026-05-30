package cli

import (
	"context"
	"fmt"
	"strings"

	"grove/internal/grove"
)

// connectArgs is the parsed shape of a /connect command. It covers every
// supported source type; only the fields relevant to a given type are read.
type connectArgs struct {
	Type       string // local | obsidian | gdrive | confluence
	Path       string // local/obsidian
	FolderID   string // gdrive
	SpaceKey   string // confluence
	Site       string // confluence
	Name       string
	Collection string
	AndSync    bool
}

// isConnectCmd reports whether line is a /connect command and parses the args.
// Like /workspace and /build, /connect reaches the Grove handle, so the
// adapter handles it rather than handleReplCommand.
//
// Syntax (positional + simple --flag form):
//
//	/connect local <path> [--name X] [--collection X]
//	/connect obsidian <path> [--name X]
//	/connect gdrive [--folder ID] [--name X] [--collection X]
//	/connect confluence [--space KEY] [--site URL] [--name X]
func isConnectCmd(line string) (connectArgs, bool, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return connectArgs{}, false, nil
	}
	if c := "/" + strings.TrimLeft(fields[0], "/:"); c != "/connect" {
		return connectArgs{}, false, nil
	}
	if len(fields) < 2 {
		return connectArgs{}, true, fmt.Errorf("usage: /connect <local|obsidian|gdrive|confluence> [args]")
	}
	a := connectArgs{Type: strings.ToLower(fields[1])}
	switch a.Type {
	case "local", "obsidian", "gdrive", "confluence":
	default:
		return a, true, fmt.Errorf("unknown source type %q (use local, obsidian, gdrive, or confluence)", a.Type)
	}
	// Walk remaining tokens. First non-flag positional becomes Path/FolderID/etc.
	rest := fields[2:]
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		switch {
		case tok == "--name" && i+1 < len(rest):
			a.Name = rest[i+1]
			i++
		case tok == "--collection" && i+1 < len(rest):
			a.Collection = rest[i+1]
			i++
		case tok == "--folder" && i+1 < len(rest):
			a.FolderID = rest[i+1]
			i++
		case tok == "--space" && i+1 < len(rest):
			a.SpaceKey = rest[i+1]
			i++
		case tok == "--site" && i+1 < len(rest):
			a.Site = rest[i+1]
			i++
		case tok == "--and-sync":
			a.AndSync = true
		case strings.HasPrefix(tok, "--"):
			return a, true, fmt.Errorf("unknown flag %q", tok)
		default:
			// First bare positional → primary arg for the type.
			switch a.Type {
			case "local", "obsidian":
				if a.Path == "" {
					a.Path = tok
				}
			case "gdrive":
				if a.FolderID == "" {
					a.FolderID = tok
				}
			case "confluence":
				if a.SpaceKey == "" {
					a.SpaceKey = tok
				}
			}
		}
	}
	switch a.Type {
	case "local", "obsidian":
		if a.Path == "" {
			return a, true, fmt.Errorf("/connect %s requires a path", a.Type)
		}
	}
	return a, true, nil
}

// runConnect dispatches a parsed /connect to the right Grove method, threading
// onProgress through. Returns the connect result for the caller to render.
func runConnect(ctx context.Context, g *grove.Grove, a connectArgs, onProgress func(grove.IngestProgress)) (*grove.ConnectResult, error) {
	opts := grove.ConnectOpts{
		Path:       a.Path,
		Name:       a.Name,
		Collection: a.Collection,
		FolderID:   a.FolderID,
		AndSync:    a.AndSync,
		OnProgress: onProgress,
	}
	switch a.Type {
	case "local":
		return g.ConnectLocal(ctx, opts)
	case "obsidian":
		return g.ConnectObsidian(ctx, opts)
	case "gdrive":
		return g.ConnectGDrive(ctx, opts)
	case "confluence":
		return g.ConnectConfluence(ctx, opts, a.SpaceKey, a.Site)
	}
	return nil, fmt.Errorf("internal: unhandled connect type %q", a.Type)
}

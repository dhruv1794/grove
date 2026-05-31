package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"grove/internal/core"
	"grove/internal/grove"
)

// progressEvent is the SSE `progress` payload shared by build/sync/embed/connect.
type progressEvent struct {
	Op     string `json:"op"`
	Source string `json:"source"`
	Total  int    `json:"total"`
	Done   int    `json:"done"`
	Doc    string `json:"doc,omitempty"`
}

// sseOp runs a long operation, streaming `progress` events as it goes and a
// terminal `done` (the result) or `error`. The operation runs in its own
// goroutine and reports progress through a channel; only this handler goroutine
// writes to the ResponseWriter, so the operation's worker goroutines (which may
// invoke the progress callback) never race on the stream. Client disconnect
// cancels the operation via the request context.
func (s *Server) sseOp(w http.ResponseWriter, r *http.Request, op func(ctx context.Context, emit func(progressEvent)) (any, error)) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, core.NewError(core.KindGeneric, "streaming unsupported by this server", ""))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	type ev struct {
		event string
		data  any
	}
	ch := make(chan ev, 256)
	go func() {
		emit := func(p progressEvent) {
			select {
			case ch <- ev{"progress", p}:
			case <-r.Context().Done():
			}
		}
		res, err := op(r.Context(), emit)
		if err != nil {
			ch <- ev{"error", errBody{Code: kindOf(err), Message: err.Error(), Remediation: remediationOf(err)}}
		} else {
			ch <- ev{"done", res}
		}
		close(ch)
	}()

	for e := range ch {
		b, err := json.Marshal(e.data)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.event, b)
		flusher.Flush()
	}
}

type buildReq struct {
	Model       string `json:"model"`
	Source      string `json:"source"`
	Rebuild     bool   `json:"rebuild"`
	NoGroup     bool   `json:"no_group"`
	DryRun      bool   `json:"dry_run"`
	Concurrency int    `json:"concurrency"`
	Compress    string `json:"compress"`
}

func (s *Server) handleBuild(w http.ResponseWriter, r *http.Request) {
	var req buildReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, core.NewError(core.KindMisuse, "invalid request body", "POST JSON {model, source, rebuild, no_group, dry_run, concurrency}"))
		return
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 4 // match the CLI default
	}
	s.sseOp(w, r, func(ctx context.Context, emit func(progressEvent)) (any, error) {
		return s.g.Build(ctx, grove.BuildOpts{
			Model:       req.Model,
			Source:      req.Source,
			Rebuild:     req.Rebuild,
			NoGroup:     req.NoGroup,
			DryRun:      req.DryRun,
			Concurrency: req.Concurrency,
			Compress:    req.Compress,
			OnProgress: func(p grove.BuildProgress) {
				emit(progressEvent{Op: "build", Source: p.Source, Total: p.Total, Done: p.Done})
			},
		})
	})
}

type syncReq struct {
	Model  string `json:"model"`
	Source string `json:"source"`
	Force  bool   `json:"force"`
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var req syncReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, core.NewError(core.KindMisuse, "invalid request body", "POST JSON {model, source, force}"))
		return
	}
	s.sseOp(w, r, func(ctx context.Context, emit func(progressEvent)) (any, error) {
		return s.g.Sync(ctx, grove.SyncOpts{
			Model:  req.Model,
			Source: req.Source,
			Force:  req.Force,
			OnProgress: func(p grove.BuildProgress) {
				emit(progressEvent{Op: "sync", Source: p.Source, Total: p.Total, Done: p.Done})
			},
		})
	})
}

type embedReq struct {
	Model    string `json:"model"`
	Source   string `json:"source"`
	MaxChars int    `json:"max_chars"`
	Chunks   bool   `json:"chunks"`
	Compress string `json:"compress"`
}

func (s *Server) handleEmbed(w http.ResponseWriter, r *http.Request) {
	var req embedReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, core.NewError(core.KindMisuse, "invalid request body", "POST JSON {model, source, max_chars, chunks}"))
		return
	}
	s.sseOp(w, r, func(ctx context.Context, emit func(progressEvent)) (any, error) {
		return s.g.Embed(ctx, grove.EmbedOpts{
			Model:    req.Model,
			Source:   req.Source,
			MaxChars: req.MaxChars,
			Chunks:   req.Chunks,
			Compress: req.Compress,
			OnProgress: func(p grove.EmbedProgress) {
				emit(progressEvent{Op: "embed", Source: p.Source, Total: p.Total, Done: p.Done})
			},
		})
	})
}

// connectResultDTO is the API view of a connect (core ConnectResult has no JSON
// tags; this keeps the wire shape lowercase and stable).
type connectResultDTO struct {
	Name      string             `json:"name"`
	Path      string             `json:"path"`
	DocCount  int                `json:"doc_count"`
	Refetched int                `json:"refetched"`
	Skipped   int                `json:"skipped"`
	Existed   bool               `json:"existed"`
	Sync      *grove.SyncResult  `json:"sync,omitempty"`
}

type connectReq struct {
	Type       string   `json:"type"` // local | obsidian | gdrive | confluence
	Path       string   `json:"path"`
	Name       string   `json:"name"`
	Collection string   `json:"collection"`
	Include    []string `json:"include"`
	Exclude    []string `json:"exclude"`
	MaxSizeMB  int64    `json:"max_size_mb"`
	FolderID   string   `json:"folder_id"` // gdrive
	SpaceKey   string   `json:"space_key"` // confluence
	Site       string   `json:"site"`      // confluence
	AndSync    bool     `json:"and_sync"`
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req connectReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, core.NewError(core.KindMisuse, "invalid request body", "POST JSON {type, path, name, …}"))
		return
	}
	s.sseOp(w, r, func(ctx context.Context, emit func(progressEvent)) (any, error) {
		opts := grove.ConnectOpts{
			Path:       req.Path,
			Name:       req.Name,
			Collection: req.Collection,
			Include:    req.Include,
			Exclude:    req.Exclude,
			MaxSizeMB:  req.MaxSizeMB,
			FolderID:   req.FolderID,
			AndSync:    req.AndSync,
			OnProgress: func(p grove.IngestProgress) {
				emit(progressEvent{Op: "connect", Source: p.Source, Total: p.Total, Done: p.Done, Doc: p.Doc})
			},
		}
		var res *grove.ConnectResult
		var err error
		switch req.Type {
		case "local":
			res, err = s.g.ConnectLocal(ctx, opts)
		case "obsidian":
			res, err = s.g.ConnectObsidian(ctx, opts)
		case "gdrive":
			// OAuth: the first connect opens the user's browser for consent.
			res, err = s.g.ConnectGDrive(ctx, opts)
		case "confluence":
			res, err = s.g.ConnectConfluence(ctx, opts, req.SpaceKey, req.Site)
		default:
			return nil, core.NewError(core.KindMisuse,
				"unknown source type "+req.Type,
				"use one of: local, obsidian, gdrive, confluence")
		}
		if err != nil {
			return nil, err
		}
		return connectResultDTO{
			Name: res.Name, Path: res.Path, DocCount: res.DocCount,
			Refetched: res.Refetched, Skipped: res.Skipped, Existed: res.Existed, Sync: res.Sync,
		}, nil
	})
}

// remediationOf returns the remediation string when err is a *core.Error.
func remediationOf(err error) string {
	var ge *core.Error
	if errors.As(err, &ge) {
		return ge.Remediation
	}
	return ""
}

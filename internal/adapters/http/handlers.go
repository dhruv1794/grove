package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"grove/internal/core"
	"grove/internal/grove"
)

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	report, err := s.g.Status(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	// Reuse the status snapshot's per-source view: it carries doc counts and
	// omits the raw connector ConfigJSON blob (which would leak local paths).
	report, err := s.g.Status(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report.Sources)
}

func (s *Server) handleTrees(w http.ResponseWriter, r *http.Request) {
	trees, err := s.g.ForestView(r.Context(), depthParam(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trees)
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	graph, err := s.g.GraphView(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	tree, err := s.g.TreeByID(r.Context(), r.PathValue("id"), depthParam(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

// docDTO is the API view of a document: the fields the UI needs, with stable
// lowercase JSON, omitting internal connector metadata.
type docDTO struct {
	ID        string   `json:"id"`
	Source    string   `json:"source"`
	SourceRef string   `json:"source_ref"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Hierarchy []string `json:"hierarchy,omitempty"`
}

func (s *Server) handleDoc(w http.ResponseWriter, r *http.Request) {
	doc, err := s.g.Document(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, docDTO{
		ID: doc.ID, Source: doc.Source, SourceRef: doc.SourceRef,
		Title: doc.Title, Content: doc.Content, Hierarchy: doc.Hierarchy,
	})
}

// askReq is the POST /api/ask body. Mode maps to retrieval stages via
// grove.StagesForMode; an empty mode defaults to balanced.
type askReq struct {
	Query        string `json:"query"`
	Mode         string `json:"mode"`
	Source       string `json:"source"`
	RetrieveOnly bool   `json:"retrieve_only"`
}

// handleAsk answers a question, streaming the synthesized answer token-by-token
// as Server-Sent Events. Event stream: zero or more `token` events (each
// {"text": "..."}), then exactly one terminal `done` (the full query.Result) or
// `error` ({"message": "..."}). The query model comes from config [query].model
// (the web UI doesn't pick models per request yet).
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req askReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, core.NewError(core.KindMisuse, "invalid request body", "POST JSON {query, mode, source, retrieve_only}"))
		return
	}
	if req.Query == "" {
		writeErr(w, core.NewError(core.KindMisuse, "empty query", "include a non-empty \"query\""))
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "balanced"
	}
	st, err := grove.StagesForMode(mode)
	if err != nil {
		writeErr(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, core.NewError(core.KindGeneric, "streaming unsupported by this server", ""))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering if one is in front
	w.WriteHeader(http.StatusOK)

	send := func(event string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	res, err := s.g.Ask(r.Context(), grove.AskOpts{
		Query:        req.Query,
		Source:       req.Source,
		RetrieveOnly: req.RetrieveOnly,
		Fast:         st.Fast,
		Decompose:    st.Decompose,
		Rerank:       st.Rerank,
		OnToken:      func(t string) { send("token", map[string]string{"text": t}) },
	})
	if err != nil {
		send("error", errBody{Code: kindOf(err), Message: err.Error()})
		return
	}
	send("done", res)
}

// kindOf returns the grove error kind string when err is a *core.Error, else
// the generic kind.
func kindOf(err error) string {
	var ge *core.Error
	if errors.As(err, &ge) {
		return string(ge.Kind)
	}
	return string(core.KindGeneric)
}

// depthParam reads an optional ?depth=N (tree render depth below the root); 0
// or absent means the full tree.
func depthParam(r *http.Request) int {
	var d int
	if v := r.URL.Query().Get("depth"); v != "" {
		json.Unmarshal([]byte(v), &d) // best-effort; bad value → 0 (full tree)
	}
	return d
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Header is already sent; nothing useful to do but stop.
		return
	}
}

// errBody is the JSON error envelope, matching the CLI's --json error contract
// ({code, message, remediation}) so both adapters speak the same shape.
type errBody struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

// writeErr renders a structured grove error as JSON with a mapped HTTP status.
func writeErr(w http.ResponseWriter, err error) {
	var ge *core.Error
	if errors.As(err, &ge) {
		writeJSON(w, statusForKind(ge.Kind), errBody{
			Code: string(ge.Kind), Message: ge.Message, Remediation: ge.Remediation,
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, errBody{Code: string(core.KindGeneric), Message: err.Error()})
}

// statusForKind maps a grove error kind to an HTTP status.
func statusForKind(k core.ErrorKind) int {
	switch k {
	case core.KindMisuse:
		return http.StatusBadRequest
	case core.KindNoWorkspace, core.KindNoSources:
		return http.StatusNotFound
	case core.KindModelUnreachable, core.KindSourceUnreachable:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

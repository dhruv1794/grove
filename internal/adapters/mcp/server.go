// Package mcp exposes a grove forest as a Model Context Protocol server over
// stdio. It is a thin adapter over internal/grove: every tool, resource, and
// prompt delegates straight to the core API and translates the result into MCP
// shapes. No business logic, no Store access — same contract as the HTTP adapter.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"grove/internal/core"
	"grove/internal/grove"
	"grove/internal/llm"
	"grove/internal/query"
)

// Server wraps a configured MCP server bound to one grove forest.
type Server struct {
	g   *grove.Grove
	srv *mcpsdk.Server
}

const instructions = "grove serves a knowledge forest: source-native trees of " +
	"documents with LLM summaries and cross-links. Call grove_list_trees first to " +
	"orient, then either drive navigation yourself (grove_navigate → grove_read) or " +
	"let grove walk the forest for you with grove_search. grove_search synthesizes a " +
	"cited answer using grove's own configured query model."

// New builds an MCP server over the forest. version is advertised to clients.
func New(g *grove.Grove, version string) *Server {
	srv := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "grove", Title: "grove", Version: version},
		&mcpsdk.ServerOptions{Instructions: instructions},
	)
	s := &Server{g: g, srv: srv}
	s.registerTools()
	s.registerResources()
	s.registerPrompts()
	return s
}

// Serve runs the server over the stdio transport until ctx is cancelled or the
// client disconnects. Nothing but the JSON-RPC stream may touch stdout while
// this runs; grove's diagnostics already go to stderr.
func (s *Server) Serve(ctx context.Context) error {
	if err := s.srv.Run(ctx, &mcpsdk.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}

func (s *Server) registerTools() {
	mcpsdk.AddTool(s.srv, &mcpsdk.Tool{
		Name:        "grove_list_trees",
		Description: "List the trees in the forest with their summaries and doc counts. Call this first to orient.",
	}, s.listTrees)

	mcpsdk.AddTool(s.srv, &mcpsdk.Tool{
		Name:        "grove_navigate",
		Description: "Drill into a tree one level at a time. Returns the current node and its direct children. Omit node_id to start at the tree root.",
	}, s.navigate)

	mcpsdk.AddTool(s.srv, &mcpsdk.Tool{
		Name:        "grove_read",
		Description: "Read the document(s) backing a node. For a hub node, gathers the leaf documents beneath it. Each document is truncated to max_chars (default 8000).",
	}, s.read)

	mcpsdk.AddTool(s.srv, &mcpsdk.Tool{
		Name:        "grove_search",
		Description: "Ask a question; grove walks the forest with its own query model and returns a synthesized, cited answer plus the navigation trace. The fastest path for casual use.",
	}, s.search)

	mcpsdk.AddTool(s.srv, &mcpsdk.Tool{
		Name:        "grove_links",
		Description: "Find related nodes elsewhere in the forest via the cross-link graph. Returns nodes linked from node_id with strength at least min_strength (default 0.5).",
	}, s.links)
}

// --- tools ---

type listTreesIn struct {
	Source string `json:"source,omitempty" jsonschema:"restrict to one source by name"`
}

type treeInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	DocCount  int    `json:"doc_count"`
	NodeCount int    `json:"node_count"`
	Summary   string `json:"summary,omitempty"`
}

type listTreesOut struct {
	Trees []treeInfo `json:"trees"`
}

func (s *Server) listTrees(ctx context.Context, _ *mcpsdk.CallToolRequest, in listTreesIn) (*mcpsdk.CallToolResult, listTreesOut, error) {
	// depth 1 keeps the nested Root small; we only read top-level identity here.
	trees, err := s.g.ForestView(ctx, 1)
	if err != nil {
		return errResult[listTreesOut](err)
	}
	out := listTreesOut{Trees: []treeInfo{}}
	for _, t := range trees {
		if in.Source != "" && t.Source != in.Source {
			continue
		}
		out.Trees = append(out.Trees, treeInfo{
			ID: t.ID, Name: t.Name, Source: t.Source,
			DocCount: t.DocCount, NodeCount: t.NodeCount, Summary: t.Summary,
		})
	}
	return result(fmt.Sprintf("%d tree(s)", len(out.Trees)), out)
}

type navigateIn struct {
	TreeID string `json:"tree_id" jsonschema:"the tree to navigate, from grove_list_trees"`
	NodeID string `json:"node_id,omitempty" jsonschema:"node to expand; omit to start at the tree root"`
}

func (s *Server) navigate(ctx context.Context, _ *mcpsdk.CallToolRequest, in navigateIn) (*mcpsdk.CallToolResult, *grove.NodeNav, error) {
	nav, err := s.g.Navigate(ctx, in.TreeID, in.NodeID)
	if err != nil {
		return errResult[*grove.NodeNav](err)
	}
	return result(fmt.Sprintf("%s — %d child(ren)", nav.Current.Title, len(nav.Children)), nav)
}

type readIn struct {
	NodeID   string `json:"node_id" jsonschema:"node whose documents to read"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"per-document truncation in characters; default 8000"`
}

func (s *Server) read(ctx context.Context, _ *mcpsdk.CallToolRequest, in readIn) (*mcpsdk.CallToolResult, *grove.NodeRead, error) {
	maxChars := in.MaxChars
	if maxChars <= 0 {
		maxChars = 8000
	}
	rd, err := s.g.NodeDocuments(ctx, in.NodeID, maxChars)
	if err != nil {
		return errResult[*grove.NodeRead](err)
	}
	return result(fmt.Sprintf("%s — %d document(s)", rd.Node.Title, len(rd.Documents)), rd)
}

type searchIn struct {
	Query      string `json:"query" jsonschema:"the question to answer"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"cap on cited documents; default 5"`
	Source     string `json:"source,omitempty" jsonschema:"restrict to one source by name"`
}

type citation struct {
	DocID     string   `json:"doc_id"`
	Title     string   `json:"title"`
	Source    string   `json:"source"`
	SourceRef string   `json:"source_ref"`
	TreePath  []string `json:"tree_path,omitempty"`
}

type searchOut struct {
	Answer    string               `json:"answer"`
	Citations []citation           `json:"citations"`
	Trace     query.RetrievalTrace `json:"retrieval_trace"`
	Cost      llm.Tally            `json:"cost"`
	Abstained bool                 `json:"abstained,omitempty"`
}

func (s *Server) search(ctx context.Context, _ *mcpsdk.CallToolRequest, in searchIn) (*mcpsdk.CallToolResult, searchOut, error) {
	if strings.TrimSpace(in.Query) == "" {
		return errResult[searchOut](core.NewError(core.KindMisuse, "empty query", "pass a question in \"query\""))
	}
	// Pattern B: grove drives the walk with its configured query model (the
	// balanced ensemble), so the tree-walk cost lands on grove, not the client.
	st, _ := grove.StagesForMode("balanced")
	res, err := s.g.Ask(ctx, grove.AskOpts{
		Query:     in.Query,
		Source:    in.Source,
		MaxDocs:   in.MaxResults, // 0 → core default
		Fast:      st.Fast,
		Decompose: st.Decompose,
		Rerank:    st.Rerank,
	})
	if err != nil {
		return errResult[searchOut](err)
	}
	out := searchOut{
		Answer: res.Answer, Trace: res.Trace, Cost: res.Cost,
		Abstained: res.Abstained, Citations: []citation{},
	}
	path := tracePath(res.Trace) // loop-invariant: the navigation breadcrumb for this answer
	for _, c := range res.Citations {
		out.Citations = append(out.Citations, citation{
			DocID: c.DocID, Title: c.Title, Source: c.Source,
			SourceRef: c.SourceRef, TreePath: path,
		})
	}
	summary := fmt.Sprintf("%d citation(s) · %d llm calls · %s",
		len(out.Citations), out.Cost.Calls, out.Cost.USDString())
	return result(summary, out)
}

type linksIn struct {
	NodeID      string  `json:"node_id" jsonschema:"node to find cross-links from"`
	MinStrength float64 `json:"min_strength,omitempty" jsonschema:"minimum edge strength 0..1; default 0.5"`
}

type linksOut struct {
	Links []grove.NodeLink `json:"links"`
}

func (s *Server) links(ctx context.Context, _ *mcpsdk.CallToolRequest, in linksIn) (*mcpsdk.CallToolResult, linksOut, error) {
	min := in.MinStrength
	if min == 0 {
		min = 0.5
	}
	got, err := s.g.NodeLinks(ctx, in.NodeID, float32(min))
	if err != nil {
		return errResult[linksOut](err)
	}
	return result(fmt.Sprintf("%d link(s)", len(got)), linksOut{Links: got})
}

// tracePath flattens the retrieval trace into a tree→node breadcrumb.
func tracePath(t query.RetrievalTrace) []string {
	var path []string
	for _, st := range t.SearchPath {
		if st.Node != "" {
			path = append(path, st.Node)
		}
	}
	return path
}

// --- result helpers ---

// result returns a tool result carrying a one-line human summary as text plus
// the typed value as structured content (the SDK fills StructuredContent from
// the returned Out).
func result[T any](summary string, out T) (*mcpsdk.CallToolResult, T, error) {
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: summary}},
	}, out, nil
}

// errResult surfaces a grove error as a tool-level error (IsError) with the
// structured {code,message,remediation} envelope grove uses everywhere, rather
// than a protocol error — so the model can see and reason about the failure.
func errResult[T any](err error) (*mcpsdk.CallToolResult, T, error) {
	var zero T
	code, msg, remediation := core.KindGeneric, err.Error(), ""
	var ge *core.Error
	if errors.As(err, &ge) {
		code, msg, remediation = ge.Kind, ge.Message, ge.Remediation
	}
	text := fmt.Sprintf("error [%s]: %s", code, msg)
	if remediation != "" {
		text += "\nhint: " + remediation
	}
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
	}, zero, nil
}

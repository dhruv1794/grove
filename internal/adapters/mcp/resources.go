package mcp

import (
	"context"
	"encoding/json"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"grove/internal/core"
)

func (s *Server) registerResources() {
	s.srv.AddResource(&mcpsdk.Resource{
		URI:         "grove://forest",
		Name:        "forest",
		Title:       "Forest overview",
		Description: "Top-level summary of the whole forest: every tree with its summary and counts.",
		MIMEType:    "application/json",
	}, s.readForest)

	// {+id} (reserved expansion) so ids containing '/' and ':' — which grove node
	// ids do — match the template; the default {id} only matches one path segment.
	s.srv.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "grove://tree/{+tree_id}",
		Name:        "tree",
		Description: "One tree rendered as a nested node structure.",
		MIMEType:    "application/json",
	}, s.readTree)

	s.srv.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "grove://node/{+node_id}",
		Name:        "node",
		Description: "A node's summary plus its direct children (uses the stable node id, not a content hash).",
		MIMEType:    "application/json",
	}, s.readNode)

	s.srv.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: "grove://doc/{+doc_id}",
		Name:        "doc",
		Description: "A document's content and metadata.",
		MIMEType:    "application/json",
	}, s.readDoc)
}

func (s *Server) readForest(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	trees, err := s.g.ForestView(ctx, 1)
	if err != nil {
		return nil, err
	}
	out := listTreesOut{Trees: []treeInfo{}}
	for _, t := range trees {
		out.Trees = append(out.Trees, treeInfo{
			ID: t.ID, Name: t.Name, Source: t.Source,
			DocCount: t.DocCount, NodeCount: t.NodeCount, Summary: t.Summary,
		})
	}
	return jsonResource(req.Params.URI, out)
}

func (s *Server) readTree(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	id := strings.TrimPrefix(req.Params.URI, "grove://tree/")
	tree, err := s.g.TreeByID(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	return jsonResource(req.Params.URI, tree)
}

func (s *Server) readNode(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	id := strings.TrimPrefix(req.Params.URI, "grove://node/")
	nav, err := s.g.NodeByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return jsonResource(req.Params.URI, nav)
}

// docResource is the resource view of a document: content + the fields a client
// needs, omitting internal connector metadata (mirrors the HTTP adapter's DTO).
type docResource struct {
	ID        string   `json:"id"`
	Source    string   `json:"source"`
	SourceRef string   `json:"source_ref"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Hierarchy []string `json:"hierarchy,omitempty"`
}

func (s *Server) readDoc(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	id := strings.TrimPrefix(req.Params.URI, "grove://doc/")
	doc, err := s.g.Document(ctx, id)
	if err != nil {
		return nil, err
	}
	return jsonResource(req.Params.URI, docResource{
		ID: doc.ID, Source: doc.Source, SourceRef: doc.SourceRef,
		Title: doc.Title, Content: doc.Content, Hierarchy: doc.Hierarchy,
	})
}

// jsonResource marshals v as the single JSON content of a resource read result.
// Compact (not indented): a resource (a whole tree can be large) is consumed by
// the model, so the indentation whitespace is wasted tokens.
func jsonResource(uri string, v any) (*mcpsdk.ReadResourceResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, core.WrapError(core.KindGeneric, err,
			"cannot encode resource "+uri, "")
	}
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{
			URI: uri, MIMEType: "application/json", Text: string(b),
		}},
	}, nil
}

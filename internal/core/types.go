package core

import "time"

type SourceType string

const (
	SourceLocal      SourceType = "local"
	SourceObsidian   SourceType = "obsidian"
	SourceGDrive     SourceType = "gdrive"
	SourceConfluence SourceType = "confluence"
)

type LinkType string

const (
	LinkWiki         LinkType = "wikilink"
	LinkURL          LinkType = "url"
	LinkDocRef       LinkType = "doc-ref"
	LinkThreadParent LinkType = "thread-parent"
)

type DocLink struct {
	Type   LinkType
	Target string
	Anchor string
}

type DocMetadata struct {
	Author    string
	Language  string
	Created   time.Time
	Modified  time.Time
	Tags      []string
	SizeBytes int64
	Custom    map[string]string
}

// Document is the normalized form every connector produces.
type Document struct {
	ID         string // sha256(source + source_ref)
	Source     string
	SourceRef  string
	Collection string
	Title      string
	Content    string
	Metadata   DocMetadata
	Links      []DocLink
	Hierarchy  []string
	Hash       string
}

type Source struct {
	Name        string
	Type        SourceType
	ConfigJSON  string
	ConnectedAt time.Time
	LastSyncAt  time.Time
}

type Collection struct {
	Source string
	Name   string
	Path   string
}

type NodeRef struct {
	NodeID   string
	TreeID   string
	Reason   string
	Strength float32
}

// Node is a tree node. ID is the stable logical identity used in
// citations and MCP URIs; ContentHash addresses the payload on disk.
type Node struct {
	ID          string
	TreeID      string
	ParentID    string
	Title       string
	Summary     string
	DocIDs      []string
	StartIdx    int
	EndIdx      int
	Children    []string
	Depth       int
	SeeAlso     []NodeRef
	BuiltBy     string
	BuiltAt     time.Time
	ContentHash string
	PayloadPath string
	PromptVer   string
}

type Tree struct {
	ID         string
	Source     string
	Collection string
	Name       string
	RootNodeID string
	DocCount   int
	NodeCount  int
	Created    time.Time
	Modified   time.Time
	BuildModel string
	PromptVer  string
}

// BuildRecord is one row of build history (the build_history table in
// documents/03-data-model.md) — a durable, queryable log of each build run.
type BuildRecord struct {
	StartedAt     time.Time
	FinishedAt    time.Time
	Model         string
	DocsProcessed int
	NodesCreated  int
	CostUSD       float64
	Status        string // BuildStatusOK today; failure statuses when build error handling lands
}

// BuildStatusOK marks a successfully completed build run.
const BuildStatusOK = "ok"

type ChangeType string

const (
	ChangeCreated  ChangeType = "created"
	ChangeModified ChangeType = "modified"
	ChangeDeleted  ChangeType = "deleted"
)

type Change struct {
	DocID    string
	Type     ChangeType
	Document *Document
}

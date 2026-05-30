// Package gdrive ingests Google Drive as a grove source. It exports Google Docs
// to markdown (with an HTML→markdown fallback) and indexes uploaded
// markdown/text/PDF/DOCX files directly. Incremental sync uses Drive's
// server-side modifiedTime, so only changed files are re-fetched. The connector
// reads Drive over an OAuth2 client built from a stored token; it never touches
// the Store, indexer, or LLMs.
package gdrive

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"

	"grove/internal/auth"
	"grove/internal/connectors"
	"grove/internal/core"
)

// Config keys carried in ConnectorConfig.Custom. auth_dir is the workspace auth
// directory (set by the adapter, derived from the live layout — never a
// persisted absolute path); folder_id optionally scopes ingest to a subtree.
// client_id / client_secret are injected by the adapter at run time
// (resolved from config + env, never persisted) so the connector can refresh
// tokens headlessly during sync.
const (
	cfgAuthDir      = "auth_dir"
	cfgFolderID     = "folder_id"
	cfgClientID     = "client_id"
	cfgClientSecret = "client_secret"
)

// DriveScope is read-only: grove never writes to a source.
const DriveScope = drive.DriveReadonlyScope

type Connector struct {
	name     string
	cfg      connectors.ConnectorConfig
	folderID string
	api      driveAPI
}

func New() *Connector { return &Connector{} }

// newWithAPI builds a connector around an injected driveAPI — the seam tests
// use to avoid real network and OAuth.
func newWithAPI(name, folderID string, api driveAPI) *Connector {
	return &Connector{name: name, folderID: folderID, api: api}
}

func (c *Connector) Name() string { return c.name }

// OAuthConfig builds the OAuth2 config from explicit BYO client credentials.
// grove does not bundle a client secret (see 04-connectors); the adapter
// resolves clientID / clientSecret from {config.toml [gdrive] block, env vars
// GROVE_GDRIVE_CLIENT_ID / GROVE_GDRIVE_CLIENT_SECRET}, with env winning.
func OAuthConfig(clientID, clientSecret string) (*oauth2.Config, error) {
	if clientID == "" || clientSecret == "" {
		return nil, core.NewError(core.KindMisuse,
			"Google Drive OAuth client credentials are not set",
			"create a Google Cloud OAuth 'Desktop app' client, then either set GROVE_GDRIVE_CLIENT_ID / GROVE_GDRIVE_CLIENT_SECRET or run `grove config init --local` and fill in the [gdrive] section")
	}
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{DriveScope},
		Endpoint:     google.Endpoint,
	}, nil
}

func (c *Connector) Connect(ctx context.Context, cfg connectors.ConnectorConfig) error {
	c.name = cfg.Name
	c.cfg = cfg
	c.folderID = cfg.Custom[cfgFolderID]

	// Tests inject the API directly; skip the OAuth/service build.
	if c.api != nil {
		return nil
	}

	authDir := cfg.Custom[cfgAuthDir]
	if authDir == "" {
		return fmt.Errorf("gdrive connector requires an auth directory")
	}
	oauthCfg, err := OAuthConfig(cfg.Custom[cfgClientID], cfg.Custom[cfgClientSecret])
	if err != nil {
		return err
	}
	store := auth.NewStore(authDir, auth.MachineID())
	if !store.Has(c.name) {
		return core.NewError(core.KindSourceUnreachable,
			fmt.Sprintf("no stored Google credentials for source %q", c.name),
			"run `grove connect gdrive` to authorize")
	}
	client, err := auth.HTTPClient(ctx, oauthCfg, store, c.name)
	if err != nil {
		return fmt.Errorf("gdrive auth: %w", err)
	}
	svc, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return fmt.Errorf("gdrive service: %w", err)
	}
	c.api = &realDrive{svc: svc}
	return nil
}

func (c *Connector) Disconnect(ctx context.Context) error { return nil }

func (c *Connector) Validate(ctx context.Context) error {
	if c.api == nil {
		return fmt.Errorf("not connected")
	}
	// Cheapest authenticated call that proves the token works.
	_, err := c.api.listFiles(ctx, "trashed = false")
	return err
}

func (c *Connector) Capabilities() connectors.Capabilities {
	return connectors.Capabilities{
		SupportsIncremental: true,
		SupportsLinks:       false,
		SupportsRichMeta:    true,
		SupportsAuth:        connectors.AuthOAuth,
		NativeFormat:        "markdown",
	}
}

// fetchFolders lists every non-trashed folder once so paths can be resolved
// without an API call per file.
func (c *Connector) fetchFolders(ctx context.Context) (map[string]folderInfo, error) {
	files, err := c.api.listFiles(ctx, fmt.Sprintf("mimeType = '%s' and trashed = false", mimeFolder))
	if err != nil {
		return nil, err
	}
	idx := make(map[string]folderInfo, len(files))
	for _, f := range files {
		idx[f.ID] = folderInfo{name: f.Name, parents: f.Parents}
	}
	return idx, nil
}

// fileQuery is the base query for ingestible (non-folder, non-trashed) files,
// optionally restricted to those modified after since.
func fileQuery(since time.Time) string {
	q := fmt.Sprintf("mimeType != '%s' and trashed = false", mimeFolder)
	if !since.IsZero() {
		q += fmt.Sprintf(" and modifiedTime > '%s'", since.UTC().Format(time.RFC3339))
	}
	return q
}

// stream walks files matching the query, builds documents, and hands each to
// emit. It is shared by Documents and Changes. known is the resume gate
// (StreamOpts.KnownDocs): if a file's stable doc ID is already stored and the
// Drive-side modifiedTime is not newer, skip without exporting/downloading the
// content (the expensive call).
func (c *Connector) stream(ctx context.Context, since time.Time, known map[string]time.Time, emit func(core.Document) error) error {
	folders, err := c.fetchFolders(ctx)
	if err != nil {
		return err
	}
	files, err := c.api.listFiles(ctx, fileQuery(since))
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(known) > 0 {
			// Second-precision: store persists Modified as Unix seconds, Drive
			// returns RFC3339 with sub-second precision.
			if stored, ok := known[docID(c.name, f.ID)]; ok && f.ModifiedTime.Unix() <= stored.Unix() {
				continue
			}
		}
		doc, ok, err := buildDocument(ctx, c.api, c.name, c.cfg.Collection, folders, c.folderID, f)
		if err != nil {
			// A single unreadable/unexportable file shouldn't abort the run.
			fmt.Fprintf(os.Stderr, "grove: skipping Drive file %q (%s): %v\n", f.Name, f.ID, err)
			continue
		}
		if !ok {
			continue
		}
		if err := emit(doc); err != nil {
			return err
		}
	}
	return nil
}

func (c *Connector) Documents(ctx context.Context, opts connectors.StreamOpts) (<-chan core.Document, <-chan error) {
	docs := make(chan core.Document, 128)
	errs := make(chan error, 1)
	go func() {
		defer close(docs)
		defer close(errs)
		err := c.stream(ctx, time.Time{}, opts.KnownDocs, func(doc core.Document) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case docs <- doc:
				return nil
			}
		})
		if err != nil {
			errs <- err
		}
	}()
	return docs, errs
}

func (c *Connector) Changes(ctx context.Context, since time.Time) (<-chan core.Change, <-chan error) {
	changes := make(chan core.Change, 128)
	errs := make(chan error, 1)
	go func() {
		defer close(changes)
		defer close(errs)
		err := c.stream(ctx, since, nil, func(doc core.Document) error {
			ch := core.Change{DocID: doc.ID, Type: core.ChangeModified, Document: &doc}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case changes <- ch:
				return nil
			}
		})
		if err != nil {
			errs <- err
		}
	}()
	return changes, errs
}

// Enumerate streams the current in-scope document IDs without fetching content
// — the cheap pass sync uses for deletion detection.
func (c *Connector) Enumerate(ctx context.Context) (<-chan string, <-chan error) {
	ids := make(chan string, 128)
	errs := make(chan error, 1)
	go func() {
		defer close(ids)
		defer close(errs)
		folders, err := c.fetchFolders(ctx)
		if err != nil {
			errs <- err
			return
		}
		files, err := c.api.listFiles(ctx, fileQuery(time.Time{}))
		if err != nil {
			errs <- err
			return
		}
		for _, f := range files {
			_, inScope := resolvePath(folders, f, c.folderID)
			if !inScope {
				continue
			}
			if _, ok := supportedMime(f.MimeType); !ok {
				continue
			}
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			case ids <- docID(c.name, f.ID):
			}
		}
	}()
	return ids, errs
}

// supportedMime reports whether a mime type yields an ingestible document, so
// Enumerate's ID set matches what Documents actually emits (deletion detection
// compares the two). The bool mirrors extractContent's skip decision without
// fetching content.
func supportedMime(mt string) (string, bool) {
	switch mt {
	case mimeGDoc, mimeGSheet, mimeGSlide, mimePDF, mimeDOCX:
		return mt, true
	}
	if len(mt) >= 5 && mt[:5] == "text/" {
		return mt, true
	}
	return "", false
}

// Package confluence ingests Atlassian Confluence as a grove source. It walks
// the page tree of one or more spaces over the REST v2 API, converts the
// Atlassian storage format (XHTML + ac:/ri: macros) to Markdown, and tracks
// per-page modification time for incremental sync. Auth is OAuth 2.0 (3LO) via
// api.atlassian.com — the same browser flow as the gdrive connector. Tokens
// are encrypted at rest by internal/auth.
package confluence

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/oauth2"

	"grove/internal/auth"
	"grove/internal/connectors"
	"grove/internal/core"
)

// Config keys carried in ConnectorConfig.Custom. auth_dir is re-injected at
// sync time from the live layout (not persisted); cloud_id and site_url are
// discovered post-authorize and persisted. client_id / client_secret are
// resolved by the adapter from config + env and injected at run time (never
// persisted), so the connector can refresh tokens during sync.
const (
	cfgAuthDir      = "auth_dir"
	cfgCloudID      = "cloud_id"
	cfgSiteURL      = "site_url"
	cfgSpaceKey     = "space_key"
	cfgClientID     = "client_id"
	cfgClientSecret = "client_secret"
)

// Scopes requested by grove. classic-scope set: read pages, read spaces,
// refresh tokens. Confluence Cloud's v2 API honors these.
var Scopes = []string{
	"read:confluence-content.all",
	"read:confluence-space.summary",
	"offline_access",
}

// AtlassianEndpoint is the OAuth 2.0 (3LO) endpoint for Atlassian Cloud.
var AtlassianEndpoint = oauth2.Endpoint{
	AuthURL:  "https://auth.atlassian.com/authorize",
	TokenURL: "https://auth.atlassian.com/oauth/token",
}

// AudienceParam is required on the authorize URL for Atlassian's OAuth 2.0
// (3LO) flow; the access token is scoped to api.atlassian.com.
const AudienceParam = "api.atlassian.com"

// CallbackPort is the fixed loopback port grove uses for the Atlassian OAuth
// callback. Atlassian requires the callback URL match the pre-registered
// value exactly (including port), so we pin one and the user registers
// http://127.0.0.1:53682/callback in their OAuth app. 53682 is the rclone
// convention — a free, registered port unlikely to collide.
const CallbackPort = 53682

type Connector struct {
	name     string
	cfg      connectors.ConnectorConfig
	cloudID  string
	siteURL  string
	spaceKey string
	api      confluenceAPI
}

func New() *Connector { return &Connector{} }

// newWithAPI builds a connector around an injected confluenceAPI — the seam
// tests use to avoid real network.
func newWithAPI(name, spaceKey string, api confluenceAPI) *Connector {
	return &Connector{name: name, spaceKey: spaceKey, api: api}
}

func (c *Connector) Name() string { return c.name }

// OAuthConfig builds the OAuth2 config from explicit BYO Atlassian client
// credentials. The adapter resolves clientID / clientSecret from
// {config.toml [confluence] block, env vars GROVE_CONFLUENCE_CLIENT_ID /
// GROVE_CONFLUENCE_CLIENT_SECRET}, with env winning.
func OAuthConfig(clientID, clientSecret string) (*oauth2.Config, error) {
	if clientID == "" || clientSecret == "" {
		return nil, core.NewError(core.KindMisuse,
			"Confluence OAuth client credentials are not set",
			"create an Atlassian OAuth 2.0 (3LO) app at developer.atlassian.com, then either set GROVE_CONFLUENCE_CLIENT_ID / GROVE_CONFLUENCE_CLIENT_SECRET or run `grove config init --local` and fill in the [confluence] section")
	}
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       Scopes,
		Endpoint:     AtlassianEndpoint,
	}, nil
}

func (c *Connector) Connect(ctx context.Context, cfg connectors.ConnectorConfig) error {
	c.name = cfg.Name
	c.cfg = cfg
	c.cloudID = cfg.Custom[cfgCloudID]
	c.siteURL = cfg.Custom[cfgSiteURL]
	c.spaceKey = cfg.Custom[cfgSpaceKey]

	if c.api != nil {
		return nil
	}
	if c.cloudID == "" {
		return fmt.Errorf("confluence connector requires a cloud_id (run `grove connect confluence` first)")
	}
	authDir := cfg.Custom[cfgAuthDir]
	if authDir == "" {
		return fmt.Errorf("confluence connector requires an auth directory")
	}
	oauthCfg, err := OAuthConfig(cfg.Custom[cfgClientID], cfg.Custom[cfgClientSecret])
	if err != nil {
		return err
	}
	store := auth.NewStore(authDir, auth.MachineID())
	if !store.Has(c.name) {
		return core.NewError(core.KindSourceUnreachable,
			fmt.Sprintf("no stored Confluence credentials for source %q", c.name),
			"run `grove connect confluence` to authorize")
	}
	client, err := auth.HTTPClient(ctx, oauthCfg, store, c.name)
	if err != nil {
		return fmt.Errorf("confluence auth: %w", err)
	}
	base := fmt.Sprintf("https://api.atlassian.com/ex/confluence/%s/wiki", c.cloudID)
	c.api = newHTTPAPIWithSite(base, c.siteURL, client)
	return nil
}

func (c *Connector) Disconnect(ctx context.Context) error { return nil }

func (c *Connector) Validate(ctx context.Context) error {
	if c.api == nil {
		return fmt.Errorf("not connected")
	}
	_, err := c.api.listSpaces(ctx)
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

// inScopeSpaces returns the spaces to ingest. If a space_key was configured,
// only that space; otherwise every space the token can read.
func (c *Connector) inScopeSpaces(ctx context.Context) ([]confluenceSpace, error) {
	all, err := c.api.listSpaces(ctx)
	if err != nil {
		return nil, err
	}
	if c.spaceKey == "" {
		return all, nil
	}
	for _, s := range all {
		if s.Key == c.spaceKey {
			return []confluenceSpace{s}, nil
		}
	}
	return nil, fmt.Errorf("space %q not found", c.spaceKey)
}

// stream walks each in-scope space's pages and hands every (filtered) page to
// emit. known is the resume gate (StreamOpts.KnownDocs): if a page's stable
// doc ID is already stored and version.createdAt is not newer, skip without
// fetching the storage-format body (the expensive call).
func (c *Connector) stream(ctx context.Context, since time.Time, known map[string]time.Time, emit func(core.Document) error) error {
	spaces, err := c.inScopeSpaces(ctx)
	if err != nil {
		return err
	}
	for _, sp := range spaces {
		// Two passes per space: a cheap metadata listing (used both as the
		// changed-set and as the full ancestor index for hierarchy), then a
		// body fetch per changed page.
		fullMeta, err := c.api.listPageMeta(ctx, sp.ID, time.Time{})
		if err != nil {
			return err
		}
		index := make(map[string]confluencePage, len(fullMeta))
		for _, p := range fullMeta {
			index[p.ID] = p
		}
		var changed []confluencePage
		if since.IsZero() {
			changed = fullMeta
		} else {
			for _, p := range fullMeta {
				if p.Modified.After(since) {
					changed = append(changed, p)
				}
			}
		}
		for _, meta := range changed {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(known) > 0 {
				// Second-precision (Confluence returns sub-second; store is seconds).
				if stored, ok := known[docID(c.name, meta.ID)]; ok && meta.Modified.Unix() <= stored.Unix() {
					continue
				}
			}
			full, err := c.api.getPageBody(ctx, meta.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "grove: skipping Confluence page %q (%s): %v\n", meta.Title, meta.ID, err)
				continue
			}
			doc, err := buildDocument(c.name, c.cfg.Collection, sp.Name, index, full)
			if err != nil {
				fmt.Fprintf(os.Stderr, "grove: skipping Confluence page %q (%s): %v\n", meta.Title, meta.ID, err)
				continue
			}
			if err := emit(doc); err != nil {
				return err
			}
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

func (c *Connector) Enumerate(ctx context.Context) (<-chan string, <-chan error) {
	ids := make(chan string, 128)
	errs := make(chan error, 1)
	go func() {
		defer close(ids)
		defer close(errs)
		spaces, err := c.inScopeSpaces(ctx)
		if err != nil {
			errs <- err
			return
		}
		for _, sp := range spaces {
			pages, err := c.api.listPageMeta(ctx, sp.ID, time.Time{})
			if err != nil {
				errs <- err
				return
			}
			for _, p := range pages {
				select {
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				case ids <- docID(c.name, p.ID):
				}
			}
		}
	}()
	return ids, errs
}

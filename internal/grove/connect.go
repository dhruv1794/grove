package grove

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"grove/internal/auth"
	"grove/internal/connectors"
	"grove/internal/connectors/confluence"
	"grove/internal/connectors/gdrive"
	"grove/internal/connectors/local"
	"grove/internal/connectors/obsidian"
	"grove/internal/core"
)

// ConnectOpts configures a connect call. The local and obsidian connectors
// take the same shape (obsidian extends local); cloud connectors ignore the
// path/glob fields and read their own.
type ConnectOpts struct {
	Path       string
	Name       string // optional; defaults to "<type>-<basename>"
	Collection string
	Include    []string
	Exclude    []string
	MaxSizeMB  int64
	FolderID   string // gdrive: optional Drive folder to scope ingest to

	// AndSync, when set, runs a full `sync` against this source right after
	// the ingest finishes — picks up deletions and rebuilds the affected
	// branches. Connect alone is ingest-only; AndSync makes it
	// reconcile-and-rebuild in one go. Equivalent to running `grove connect …`
	// then `grove sync --source <name>`.
	AndSync bool

	// OnProgress, if set, is called as each document is fetched during connect.
	// Total comes from a cheap Enumerate() pass before streaming Documents;
	// Done increments per doc. Lets adapters render a progress bar while the
	// connector grinds through a large source (e.g. thousands of Drive files).
	OnProgress func(IngestProgress)
}

// IngestProgress reports per-source document-fetch progress during connect.
type IngestProgress struct {
	Source string
	Total  int
	Done   int
	Doc    string // the document just received (Title or SourceRef)
}

// ConnectResult reports the outcome of a connect call.
type ConnectResult struct {
	Name      string
	Path      string
	DocCount  int  // total in-source docs grove now has
	Refetched int  // docs the connector actually fetched (new or changed)
	Skipped   int  // already-current docs skipped (resume / incremental)
	Existed   bool // source row pre-existed at the start of this connect

	// Sync, if non-nil, is the result of an --and-sync run after the ingest.
	// Carries the full reconciliation (deletions detected + incremental
	// rebuild), since connect alone never deletes or rebuilds.
	Sync *SyncResult
}

// ConnectLocal connects a local folder as a source.
func (g *Grove) ConnectLocal(ctx context.Context, opts ConnectOpts) (*ConnectResult, error) {
	return g.connectSource(ctx, local.New(), core.SourceLocal, "local", opts)
}

// ConnectObsidian connects an Obsidian vault as a source. Markdown only;
// frontmatter and inline #tags become Document tags; wikilinks feed the build
// cross-link pass.
func (g *Grove) ConnectObsidian(ctx context.Context, opts ConnectOpts) (*ConnectResult, error) {
	return g.connectSource(ctx, obsidian.New(), core.SourceObsidian, "obsidian", opts)
}

// connectSource connects a filesystem-backed connector, records the source,
// and indexes its documents into the store in a single batch.
func (g *Grove) connectSource(ctx context.Context, conn connectors.Connector, srcType core.SourceType, namePrefix string, opts ConnectOpts) (*ConnectResult, error) {
	abs, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, err
	}
	name := opts.Name
	if name == "" {
		name = namePrefix + "-" + filepath.Base(abs)
	}

	cfg := connectors.ConnectorConfig{
		Name:       name,
		Collection: opts.Collection,
		Include:    opts.Include,
		Exclude:    opts.Exclude,
		MaxSizeMB:  opts.MaxSizeMB,
		Custom:     map[string]string{"path": abs},
	}
	if err := conn.Connect(ctx, cfg); err != nil {
		return nil, err
	}
	res, err := g.ingestSource(ctx, conn, name, srcType, cfg, abs, opts.OnProgress)
	if err != nil {
		return nil, err
	}
	if res.Sync, err = g.maybeAndSync(ctx, name, opts); err != nil {
		return nil, err
	}
	return res, nil
}

// ConnectGDrive connects a Google Drive source. It runs the interactive browser
// OAuth flow if no token is stored for this source name yet, then ingests the
// in-scope files. Folder-scoping via opts.FolderID is honored; without it, the
// full drive the user authorized is indexed.
func (g *Grove) ConnectGDrive(ctx context.Context, opts ConnectOpts) (*ConnectResult, error) {
	name := opts.Name
	if name == "" {
		name = "gdrive"
	}
	gcfg, err := core.LoadMergedConfig(g.configPath)
	if err != nil {
		return nil, err
	}
	// Folder defaulting: opts wins, then config's default_folder.
	if opts.FolderID == "" {
		opts.FolderID = gcfg.Gdrive.DefaultFolder
	}
	oauthCfg, err := gdrive.OAuthConfig(gcfg.Gdrive.ClientID, gcfg.Gdrive.ClientSecret)
	if err != nil {
		return nil, err
	}
	store := auth.NewStore(g.layout.Auth, auth.MachineID())
	if !store.Has(name) {
		tok, err := auth.Authorize(ctx, oauthCfg, 0)
		if err != nil {
			return nil, fmt.Errorf("gdrive authorize: %w", err)
		}
		if err := store.Save(name, tok); err != nil {
			return nil, err
		}
	}

	// Persisted config carries only folder_id; auth_dir is re-injected by
	// connectorFor from the live layout, so a moved workspace still resolves.
	persistedCustom := map[string]string{}
	if opts.FolderID != "" {
		persistedCustom["folder_id"] = opts.FolderID
	}
	cfgPersisted := connectors.ConnectorConfig{
		Name:       name,
		Collection: opts.Collection,
		Custom:     persistedCustom,
	}
	cfgLive := cfgPersisted
	cfgLive.Custom = map[string]string{
		"auth_dir":      g.layout.Auth,
		"folder_id":     opts.FolderID,
		"client_id":     gcfg.Gdrive.ClientID,
		"client_secret": gcfg.Gdrive.ClientSecret,
	}
	conn := gdrive.New()
	if err := conn.Connect(ctx, cfgLive); err != nil {
		return nil, err
	}
	res, err := g.ingestSource(ctx, conn, name, core.SourceGDrive, cfgPersisted, "", opts.OnProgress)
	if err != nil {
		return nil, err
	}
	if res.Sync, err = g.maybeAndSync(ctx, name, opts); err != nil {
		return nil, err
	}
	return res, nil
}

// ConnectConfluence connects a Confluence Cloud site over OAuth 2.0 (3LO).
// The browser flow runs once per source name; the token is encrypted in the
// workspace auth/ directory. After the exchange we call accessible-resources
// to discover the user's site(s) and persist the chosen cloudId + site URL so
// sync can reconnect headlessly.
func (g *Grove) ConnectConfluence(ctx context.Context, opts ConnectOpts, spaceKey, sitePrefer string) (*ConnectResult, error) {
	name := opts.Name
	if name == "" {
		if spaceKey != "" {
			name = "confluence-" + spaceKey
		} else {
			name = "confluence"
		}
	}
	gcfg, err := core.LoadMergedConfig(g.configPath)
	if err != nil {
		return nil, err
	}
	oauthCfg, err := confluence.OAuthConfig(gcfg.Confluence.ClientID, gcfg.Confluence.ClientSecret)
	if err != nil {
		return nil, err
	}
	store := auth.NewStore(g.layout.Auth, auth.MachineID())
	if !store.Has(name) {
		tok, err := auth.Authorize(ctx, oauthCfg, confluence.CallbackPort,
			oauth2.SetAuthURLParam("audience", confluence.AudienceParam))
		if err != nil {
			return nil, fmt.Errorf("confluence authorize: %w", err)
		}
		if err := store.Save(name, tok); err != nil {
			return nil, err
		}
	}

	client, err := auth.HTTPClient(ctx, oauthCfg, store, name)
	if err != nil {
		return nil, fmt.Errorf("confluence auth: %w", err)
	}
	resources, err := confluence.AccessibleResources(ctx, client)
	if err != nil {
		return nil, err
	}
	if len(resources) == 0 {
		return nil, fmt.Errorf("the authorized account has no accessible Confluence sites")
	}
	picked := pickSite(resources, sitePrefer)
	if picked == nil {
		return nil, fmt.Errorf("no Confluence site matched %q (available: %s)", sitePrefer, siteNames(resources))
	}
	fmt.Fprintf(os.Stderr, "grove: using Confluence site %q (%s)\n", picked.Name, picked.URL)

	persistedCustom := map[string]string{
		"cloud_id": picked.ID,
		"site_url": picked.URL,
	}
	if spaceKey != "" {
		persistedCustom["space_key"] = spaceKey
	}
	cfgPersisted := connectors.ConnectorConfig{
		Name:       name,
		Collection: opts.Collection,
		Custom:     persistedCustom,
	}
	cfgLive := cfgPersisted
	cfgLive.Custom = map[string]string{
		"auth_dir":      g.layout.Auth,
		"cloud_id":      picked.ID,
		"site_url":      picked.URL,
		"space_key":     spaceKey,
		"client_id":     gcfg.Confluence.ClientID,
		"client_secret": gcfg.Confluence.ClientSecret,
	}
	conn := confluence.New()
	if err := conn.Connect(ctx, cfgLive); err != nil {
		return nil, err
	}
	res, err := g.ingestSource(ctx, conn, name, core.SourceConfluence, cfgPersisted, "", opts.OnProgress)
	if err != nil {
		return nil, err
	}
	if res.Sync, err = g.maybeAndSync(ctx, name, opts); err != nil {
		return nil, err
	}
	return res, nil
}

// pickSite picks the accessible Atlassian site matching the user's preference
// (name or URL substring), or the first one when prefer is empty. Returns nil
// when prefer is set but doesn't match.
func pickSite(rs []confluence.AccessibleResource, prefer string) *confluence.AccessibleResource {
	if prefer == "" {
		return &rs[0]
	}
	prefer = strings.ToLower(prefer)
	for i := range rs {
		if strings.Contains(strings.ToLower(rs[i].URL), prefer) ||
			strings.Contains(strings.ToLower(rs[i].Name), prefer) {
			return &rs[i]
		}
	}
	return nil
}

func siteNames(rs []confluence.AccessibleResource) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = r.Name + " (" + r.URL + ")"
	}
	return strings.Join(parts, ", ")
}

// ingestSource records the source row, streams documents from the connected
// connector, and upserts them. A cheap Enumerate() pass gives a total up front
// so the adapter can render a real progress bar; the Documents() stream is
// drained doc-by-doc with onProgress fired per arrival, then a single
// batch-upsert preserves atomicity. Connectors that don't enumerate well still
// work (total falls back to 0 — the bar becomes a spinner-style counter).
func (g *Grove) ingestSource(ctx context.Context, conn connectors.Connector, name string, srcType core.SourceType, cfgToPersist connectors.ConnectorConfig, collectionPath string, onProgress func(IngestProgress)) (*ConnectResult, error) {
	cfgJSON, err := json.Marshal(cfgToPersist)
	if err != nil {
		return nil, fmt.Errorf("marshal connector config for %q: %w", name, err)
	}
	// "Existed" = the source row was already in the store at the start of this
	// connect. Reads before UpsertSource so a fresh row created by this call
	// doesn't lie about its history.
	existing, _ := g.store.ListSources(ctx)
	existed := false
	for _, s := range existing {
		if s.Name == name {
			existed = true
			break
		}
	}
	src := core.Source{
		Name:        name,
		Type:        srcType,
		ConfigJSON:  string(cfgJSON),
		ConnectedAt: time.Now(),
	}
	if err := g.store.UpsertSource(ctx, src); err != nil {
		return nil, err
	}
	if cfgToPersist.Collection != "" {
		if err := g.store.UpsertCollection(ctx, core.Collection{Source: name, Name: cfgToPersist.Collection, Path: collectionPath}); err != nil {
			return nil, err
		}
	}

	// Resume: load already-ingested docs (id → stored Modified) so the
	// connector can skip them, and so the progress bar starts at the right
	// position instead of zero.
	known, err := g.knownDocs(ctx, name)
	if err != nil {
		return nil, err
	}

	// Always enumerate so accurate doc counts make it into the result. The
	// extra round-trip (one cheap ID-only listing) is the cost; without it,
	// non-TTY runs would have no reliable way to distinguish "added" from
	// "updated" in the resume/incremental path.
	ids, errs := conn.Enumerate(ctx)
	idList, err := connectors.DrainChan(ids, errs)
	if err != nil {
		return nil, fmt.Errorf("enumerate %q: %w", name, err)
	}
	total := len(idList)
	startDone := 0
	for _, id := range idList {
		if _, ok := known[id]; ok {
			startDone++
		}
	}
	if onProgress != nil {
		onProgress(IngestProgress{Source: name, Total: total, Done: startDone})
	}

	docs, errs := conn.Documents(ctx, connectors.StreamOpts{KnownDocs: known})
	const batchSize = 50 // small enough that a mid-flight kill loses ≤50 docs of work; large enough to amortize SQLite tx overhead
	batch := make([]core.Document, 0, batchSize)
	done := startDone
	fetched := 0
	for docs != nil || errs != nil {
		select {
		case d, ok := <-docs:
			if !ok {
				docs = nil
				continue
			}
			// Adapter-side safety net: even if a connector ignores KnownDocs,
			// drop already-current docs before the upsert. Second-precision
			// compare matches what the store persists.
			if stored, ok := known[d.ID]; ok && d.Metadata.Modified.Unix() <= stored.Unix() {
				continue
			}
			batch = append(batch, d)
			done++
			fetched++
			if onProgress != nil {
				title := d.Title
				if title == "" {
					title = d.SourceRef
				}
				onProgress(IngestProgress{Source: name, Total: total, Done: done, Doc: title})
			}
			if len(batch) >= batchSize {
				if err := g.store.UpsertDocuments(ctx, batch); err != nil {
					return nil, err
				}
				batch = batch[:0]
			}
		case e, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if e != nil {
				return nil, e
			}
		}
	}
	if len(batch) > 0 {
		if err := g.store.UpsertDocuments(ctx, batch); err != nil {
			return nil, err
		}
	}
	if err := g.store.TouchSourceSync(ctx, name, time.Now()); err != nil {
		return nil, err
	}
	// total is from Enumerate (always run above). skipped = total - fetched is
	// exact: every in-scope doc either got fetched this run or was skipped as
	// unchanged. New docs (in source, not yet in store) are fetched.
	skipped := total - fetched
	if skipped < 0 {
		skipped = 0
	}
	return &ConnectResult{
		Name:      name,
		Path:      collectionPath,
		DocCount:  total,
		Refetched: fetched,
		Skipped:   skipped,
		Existed:   existed,
	}, nil
}

// maybeAndSync runs Sync against the just-connected source if opts.AndSync is
// set. The returned result is attached to the ConnectResult by callers.
// Errors from sync are wrapped so the user sees the source name.
func (g *Grove) maybeAndSync(ctx context.Context, name string, opts ConnectOpts) (*SyncResult, error) {
	if !opts.AndSync {
		return nil, nil
	}
	res, err := g.Sync(ctx, SyncOpts{Source: name, OnProgress: ingestProgressFn(opts.OnProgress).toBuildProgress()})
	if err != nil {
		return nil, fmt.Errorf("--and-sync for %q: %w", name, err)
	}
	return res, nil
}

// toBuildProgress adapts an ingest-progress callback to the build-progress
// shape Sync expects. The two are intentionally separate types but the cli
// renders both with the same schollz bar, so a single onProgress wired into
// connect can stand in for the rebuild's progress as well.
func (cb ingestProgressFn) toBuildProgress() func(BuildProgress) {
	if cb == nil {
		return nil
	}
	return func(p BuildProgress) {
		cb(IngestProgress{Source: p.Source, Total: p.Total, Done: p.Done})
	}
}

// ingestProgressFn is the named form of ConnectOpts.OnProgress so methods can
// be hung off it without touching the function literal at every call site.
type ingestProgressFn func(IngestProgress)

// knownDocs returns id → stored Modified for an existing source, used by
// ingestSource to make connect resumable and avoid re-fetching unchanged docs.
// Returns an empty map for a brand-new source (not an error).
func (g *Grove) knownDocs(ctx context.Context, source string) (map[string]time.Time, error) {
	metas, err := g.store.ListDocumentMetaBySource(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("load existing docs for %q: %w", source, err)
	}
	out := make(map[string]time.Time, len(metas))
	for _, m := range metas {
		out[m.ID] = m.Metadata.Modified
	}
	return out, nil
}

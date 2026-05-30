package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// confluencePage is the normalized subset grove ingests.
type confluencePage struct {
	ID         string
	Title      string
	SpaceID    string
	ParentID   string // page-tree parent (empty for top-level)
	StorageXML string // Atlassian storage format (XHTML)
	Modified   time.Time
	WebURL     string
}

// confluenceSpace is a Confluence space (filterable scope).
type confluenceSpace struct {
	ID   string
	Key  string
	Name string
}

// confluenceAPI is the slice of Confluence v2 the connector needs. The real
// implementation talks HTTP+Bearer against the api.atlassian.com OAuth proxy;
// tests inject a fake.
type confluenceAPI interface {
	listSpaces(ctx context.Context) ([]confluenceSpace, error)
	// listPageMeta lists pages in a space, returning metadata only (no body).
	// since filters client-side by version.createdAt > since.
	listPageMeta(ctx context.Context, spaceID string, since time.Time) ([]confluencePage, error)
	// getPageBody fetches a page's storage-format body and full metadata.
	getPageBody(ctx context.Context, pageID string) (confluencePage, error)
}

type httpAPI struct {
	// baseURL is the OAuth-proxied Confluence root, e.g.
	//   https://api.atlassian.com/ex/confluence/{cloudId}/wiki
	// API paths (`/api/v2/...`) are appended verbatim.
	baseURL string
	client  *http.Client // already wired with the OAuth Bearer token source
}

func newHTTPAPI(baseURL string, client *http.Client) *httpAPI {
	return &httpAPI{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
	}
}

func (a *httpAPI) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", a.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("confluence %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("confluence %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *httpAPI) listSpaces(ctx context.Context) ([]confluenceSpace, error) {
	type page struct {
		Results []struct {
			ID   string `json:"id"`
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"results"`
		Links struct {
			Next string `json:"next"`
		} `json:"_links"`
	}
	var out []confluenceSpace
	next := "/api/v2/spaces?limit=250"
	for next != "" {
		var p page
		if err := a.get(ctx, next, &p); err != nil {
			return nil, err
		}
		for _, s := range p.Results {
			out = append(out, confluenceSpace{ID: s.ID, Key: s.Key, Name: s.Name})
		}
		next = p.Links.Next
	}
	return out, nil
}

type pageV2 struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	SpaceID  string `json:"spaceId"`
	ParentID string `json:"parentId"`
	Version  struct {
		CreatedAt string `json:"createdAt"`
	} `json:"version"`
	Body struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
	Links struct {
		Webui string `json:"webui"`
	} `json:"_links"`
}

func (p pageV2) toPage(siteURL string, includeBody bool) confluencePage {
	t, _ := time.Parse(time.RFC3339, p.Version.CreatedAt)
	cp := confluencePage{
		ID:       p.ID,
		Title:    p.Title,
		SpaceID:  p.SpaceID,
		ParentID: p.ParentID,
		Modified: t,
	}
	if siteURL != "" && p.Links.Webui != "" {
		cp.WebURL = strings.TrimRight(siteURL, "/") + p.Links.Webui
	}
	if includeBody {
		cp.StorageXML = p.Body.Storage.Value
	}
	return cp
}

// pageBuilder lets the per-API site URL be carried through pagination without
// the connector having to know about it.
type httpAPIWithSite struct {
	*httpAPI
	siteURL string
}

func newHTTPAPIWithSite(baseURL, siteURL string, client *http.Client) *httpAPIWithSite {
	return &httpAPIWithSite{httpAPI: newHTTPAPI(baseURL, client), siteURL: siteURL}
}

func (a *httpAPIWithSite) listPageMeta(ctx context.Context, spaceID string, since time.Time) ([]confluencePage, error) {
	type pageList struct {
		Results []pageV2 `json:"results"`
		Links   struct {
			Next string `json:"next"`
		} `json:"_links"`
	}
	var out []confluencePage
	next := fmt.Sprintf("/api/v2/spaces/%s/pages?limit=250", url.PathEscape(spaceID))
	for next != "" {
		var p pageList
		if err := a.httpAPI.get(ctx, next, &p); err != nil {
			return nil, err
		}
		for _, pg := range p.Results {
			cp := pg.toPage(a.siteURL, false)
			if !since.IsZero() && !cp.Modified.After(since) {
				continue
			}
			out = append(out, cp)
		}
		next = p.Links.Next
	}
	return out, nil
}

func (a *httpAPIWithSite) getPageBody(ctx context.Context, pageID string) (confluencePage, error) {
	var p pageV2
	if err := a.httpAPI.get(ctx, fmt.Sprintf("/api/v2/pages/%s?body-format=storage", url.PathEscape(pageID)), &p); err != nil {
		return confluencePage{}, err
	}
	return p.toPage(a.siteURL, true), nil
}

// AccessibleResource is one Atlassian site the authorized user can access.
type AccessibleResource struct {
	ID     string   `json:"id"` // the cloudId
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Scopes []string `json:"scopes"`
}

// AccessibleResources discovers the Atlassian sites the access token can reach.
// Used by the adapter right after the OAuth exchange to pick the cloudId
// against which v2 API calls will be made.
func AccessibleResources(ctx context.Context, client *http.Client) ([]AccessibleResource, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.atlassian.com/oauth/token/accessible-resources", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("accessible-resources: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("accessible-resources: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out []AccessibleResource
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("accessible-resources decode: %w", err)
	}
	return out, nil
}

package gdrive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"grove/internal/connectors/extract"
	"grove/internal/connectors/htmlmd"
	"grove/internal/core"
)

const (
	mimeFolder = "application/vnd.google-apps.folder"
	mimeGDoc   = "application/vnd.google-apps.document"
	mimeGSheet = "application/vnd.google-apps.spreadsheet"
	mimeGSlide = "application/vnd.google-apps.presentation"
	mimePDF    = "application/pdf"
	mimeDOCX   = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
)

// folderInfo is a folder's name and parents, used to resolve paths.
type folderInfo struct {
	name    string
	parents []string
}

// extractContent resolves a file's ingestible text. ok is false when the file
// type is unsupported and should be skipped (not an error). Google Docs export
// as markdown, falling back to HTML→markdown when the markdown export is empty
// or fails (Google's markdown export is imperfect). Uploaded PDFs/DOCX reuse the
// shared extractors; text files pass through.
func extractContent(ctx context.Context, api driveAPI, f driveFile) (content string, ok bool, err error) {
	switch f.MimeType {
	case mimeGDoc:
		return exportGoogleDoc(ctx, api, f.ID)
	case mimeGSheet:
		b, err := api.export(ctx, f.ID, "text/csv")
		if err != nil {
			return "", false, err
		}
		return string(b), true, nil
	case mimeGSlide:
		b, err := api.export(ctx, f.ID, "text/plain")
		if err != nil {
			return "", false, err
		}
		return string(b), true, nil
	case mimePDF:
		b, err := api.download(ctx, f.ID)
		if err != nil {
			return "", false, err
		}
		text, err := extract.PDF(b)
		return text, err == nil, err
	case mimeDOCX:
		b, err := api.download(ctx, f.ID)
		if err != nil {
			return "", false, err
		}
		text, err := extract.DOCX(b)
		return text, err == nil, err
	}

	if strings.HasPrefix(f.MimeType, "text/") {
		b, err := api.download(ctx, f.ID)
		if err != nil {
			return "", false, err
		}
		return string(b), true, nil
	}
	// Other Google-apps types (forms, drawings, …) and binary types we don't
	// extract: skip rather than error.
	return "", false, nil
}

func exportGoogleDoc(ctx context.Context, api driveAPI, fileID string) (string, bool, error) {
	md, err := api.export(ctx, fileID, "text/markdown")
	if err == nil && strings.TrimSpace(string(md)) != "" {
		return string(md), true, nil
	}
	// Fallback: Google's native markdown export can be empty or imperfect;
	// HTML export is always available and we convert it ourselves.
	htmlBytes, herr := api.export(ctx, fileID, "text/html")
	if herr != nil {
		if err != nil {
			return "", false, fmt.Errorf("markdown export failed (%v) and html export failed: %w", err, herr)
		}
		return "", false, herr
	}
	converted, cerr := htmlmd.Convert(string(htmlBytes))
	if cerr != nil {
		return "", false, cerr
	}
	return converted, true, nil
}

// resolvePath returns a file's folder-path segments, walking the first-parent
// chain up the folder index. When rootID is non-empty, the file is in scope
// only if rootID appears in its parent chain, and the returned path is relative
// to rootID. Cycles (shouldn't happen in Drive) are bounded by maxDepth.
func resolvePath(folders map[string]folderInfo, f driveFile, rootID string) (segments []string, inScope bool) {
	const maxDepth = 64
	if len(f.Parents) == 0 {
		return nil, rootID == "" // a file with no parent is in scope only for whole-drive
	}
	var rev []string
	cur := f.Parents[0]
	for depth := 0; cur != "" && depth < maxDepth; depth++ {
		if cur == rootID {
			return reverse(rev), true
		}
		fi, found := folders[cur]
		if !found {
			break
		}
		rev = append(rev, fi.name)
		if len(fi.parents) == 0 {
			break
		}
		cur = fi.parents[0]
	}
	if rootID != "" {
		return nil, false // never reached the configured root folder
	}
	return reverse(rev), true
}

func reverse(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

// buildDocument fetches a file's content and assembles a core.Document. ok is
// false when the file is skipped (unsupported type).
func buildDocument(ctx context.Context, api driveAPI, source string, collection string, folders map[string]folderInfo, rootID string, f driveFile) (core.Document, bool, error) {
	segments, inScope := resolvePath(folders, f, rootID)
	if !inScope {
		return core.Document{}, false, nil
	}
	content, ok, err := extractContent(ctx, api, f)
	if err != nil {
		return core.Document{}, false, err
	}
	if !ok {
		return core.Document{}, false, nil
	}

	title := f.Name
	switch f.MimeType {
	case mimePDF, mimeDOCX:
		title = strings.TrimSuffix(title, filepath.Ext(title))
	}

	meta := core.DocMetadata{
		Modified:  f.ModifiedTime,
		SizeBytes: f.Size,
		Custom: map[string]string{
			"drive_id":  f.ID,
			"mime_type": f.MimeType,
		},
	}
	// Multi-parent docs: index the canonical (first-parent) path, surface the
	// alternate parent count in metadata (04-connectors §gdrive).
	if len(f.Parents) > 1 {
		meta.Custom["alt_parents"] = fmt.Sprintf("%d", len(f.Parents)-1)
	}

	doc := core.Document{
		ID:         docID(source, f.ID),
		Source:     source,
		SourceRef:  f.ID,
		Collection: collection,
		Title:      title,
		Content:    content,
		Metadata:   meta,
		Hierarchy:  segments,
		Hash:       sha256hex([]byte(content)),
	}
	return doc, true, nil
}

func docID(source, ref string) string {
	h := sha256.Sum256([]byte(source + "\x00" + ref))
	return hex.EncodeToString(h[:])
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

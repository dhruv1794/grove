package gdrive

import (
	"context"
	"fmt"
	"io"
	"time"

	"google.golang.org/api/drive/v3"
)

// driveFile is the normalized subset of a Drive file grove uses. Decoupling
// from *drive.File lets the connector logic be tested with a fake API.
type driveFile struct {
	ID           string
	Name         string
	MimeType     string
	Parents      []string
	ModifiedTime time.Time
	Size         int64
}

// driveAPI is the slice of Drive v3 the connector needs. The real
// implementation wraps *drive.Service; tests inject a fake.
type driveAPI interface {
	// listFiles returns every file matching the query q, following pagination.
	listFiles(ctx context.Context, q string) ([]driveFile, error)
	// export returns a Google-native file exported as mimeType (e.g. a Google
	// Doc as text/markdown or text/html).
	export(ctx context.Context, fileID, mimeType string) ([]byte, error)
	// download returns the raw bytes of an uploaded (binary) file.
	download(ctx context.Context, fileID string) ([]byte, error)
}

const fileFields = "nextPageToken, files(id, name, mimeType, parents, modifiedTime, size)"

type realDrive struct {
	svc *drive.Service
}

func (r *realDrive) listFiles(ctx context.Context, q string) ([]driveFile, error) {
	var out []driveFile
	pageToken := ""
	for {
		call := r.svc.Files.List().
			Q(q).
			Fields(fileFields).
			PageSize(1000).
			SupportsAllDrives(true).
			IncludeItemsFromAllDrives(true).
			Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("drive files.list: %w", err)
		}
		for _, f := range resp.Files {
			mt, _ := time.Parse(time.RFC3339, f.ModifiedTime)
			out = append(out, driveFile{
				ID:           f.Id,
				Name:         f.Name,
				MimeType:     f.MimeType,
				Parents:      f.Parents,
				ModifiedTime: mt,
				Size:         f.Size,
			})
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		pageToken = resp.NextPageToken
	}
}

func (r *realDrive) export(ctx context.Context, fileID, mimeType string) ([]byte, error) {
	resp, err := r.svc.Files.Export(fileID, mimeType).Context(ctx).Download()
	if err != nil {
		return nil, fmt.Errorf("drive files.export %s as %s: %w", fileID, mimeType, err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (r *realDrive) download(ctx context.Context, fileID string) ([]byte, error) {
	resp, err := r.svc.Files.Get(fileID).SupportsAllDrives(true).Context(ctx).Download()
	if err != nil {
		return nil, fmt.Errorf("drive files.get %s: %w", fileID, err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

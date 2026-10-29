package client

import (
	"context"
	"fmt"
	"net/http"
)

// An archive (Tools > Archives) is a console file store whose files live in a MOS bucket, through
// that bucket's console record (its storage provider). Archive names are unique across every
// tenant (API docs). Deleting the record deletes the archive with it (dev-docs/api-research.md).

// ArchiveBucket is an archive as the API returns it.
type ArchiveBucket struct {
	ID              int64
	Name            string
	Description     string
	StorageBucketID int64 // the console bucket record (storage provider) the files are kept in
	StorageBucket   string
	Owner           string
	Visibility      string
	IsPublic        bool
	FileCount       int64
	RawSize         int64
}

func archiveFrom(r Record) *ArchiveBucket {
	a := &ArchiveBucket{ID: int64Of(r["id"]), FileCount: int64Of(r["fileCount"]), RawSize: int64Of(r["rawSize"])}
	a.Name, _ = r["name"].(string)
	a.Description, _ = r["description"].(string)
	a.Visibility, _ = r["visibility"].(string)
	a.IsPublic, _ = r["isPublic"].(bool)
	if sp, ok := r["storageProvider"].(map[string]any); ok {
		a.StorageBucketID = int64Of(sp["id"])
		a.StorageBucket, _ = sp["name"].(string)
	}
	if o, ok := r["owner"].(map[string]any); ok {
		a.Owner, _ = o["name"].(string)
	}
	return a
}

// GetArchiveBucket reads one archive. A missing one is IsNotFound.
func (c *Client) GetArchiveBucket(ctx context.Context, id int64) (*ArchiveBucket, error) {
	p := fmt.Sprintf("/api/archives/buckets/%d", id)
	var resp struct {
		ArchiveBucket Record `json:"archiveBucket"`
	}
	if err := c.get(ctx, p, nil, &resp); err != nil {
		return nil, err
	}
	if resp.ArchiveBucket == nil {
		return nil, &APIError{Method: http.MethodGet, Path: p, StatusCode: http.StatusNotFound}
	}
	return archiveFrom(resp.ArchiveBucket), nil
}

// ListArchiveBuckets returns every archive the caller can see, in one list call.
func (c *Client) ListArchiveBuckets(ctx context.Context) ([]*ArchiveBucket, error) {
	all, err := c.ListRecords(ctx, "/api/archives/buckets", "archiveBuckets", nil)
	if err != nil {
		return nil, err
	}
	out := make([]*ArchiveBucket, len(all))
	for i, r := range all {
		out[i] = archiveFrom(r)
	}
	return out, nil
}

// FindArchiveBucket returns the archive with this name the caller can see, or nil. Names are unique.
func (c *Client) FindArchiveBucket(ctx context.Context, name string) (*ArchiveBucket, error) {
	all, err := c.ListArchiveBuckets(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range all {
		if a.Name == name {
			return a, nil
		}
	}
	return nil, nil
}

// ArchiveBucketInput creates or changes an archive. On update the storage bucket can't change.
type ArchiveBucketInput struct {
	Name, Description string
	StorageBucketID   int64
	IsPublic          bool
}

// CreateArchiveBucket creates a private archive (visible to its own tenant only).
func (c *Client) CreateArchiveBucket(ctx context.Context, in ArchiveBucketInput) (*ArchiveBucket, error) {
	body := map[string]any{"archiveBucket": map[string]any{
		"name": in.Name, "description": in.Description, "storageProvider": map[string]any{"id": in.StorageBucketID},
		"visibility": "private", "isPublic": in.IsPublic, "accounts": []any{},
	}}
	var resp struct {
		ArchiveBucket Record `json:"archiveBucket"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/archives/buckets", nil, body, &resp); err != nil {
		return nil, err
	}
	if resp.ArchiveBucket == nil || int64Of(resp.ArchiveBucket["id"]) == 0 {
		return nil, fmt.Errorf("archive create: the answer names no archive")
	}
	return archiveFrom(resp.ArchiveBucket), nil
}

// UpdateArchiveBucket changes an archive's name, description and public URL.
func (c *Client) UpdateArchiveBucket(ctx context.Context, id int64, in ArchiveBucketInput) error {
	body := map[string]any{"archiveBucket": map[string]any{"name": in.Name, "description": in.Description, "isPublic": in.IsPublic}}
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/archives/buckets/%d", id), nil, body, nil)
}

// DeleteArchiveBucket deletes an archive. One already gone is not an error.
func (c *Client) DeleteArchiveBucket(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/archives/buckets/%d", id), nil, nil, nil))
}

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
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

// ArchiveFile is a file or folder in an archive. Uploading to the same path again keeps its ID
// (dev-docs/api-research.md).
type ArchiveFile struct {
	ID          int64
	Name        string
	FilePath    string // its whole path in the archive, for example "docs/hello.txt"
	ArchiveID   int64
	Archive     string
	IsDirectory bool
	RawSize     int64
	ContentType string
	LastUpdated string
}

func archiveFileFrom(r Record) *ArchiveFile {
	f := &ArchiveFile{ID: int64Of(r["id"]), RawSize: int64Of(r["rawSize"])}
	f.Name, _ = r["name"].(string)
	f.FilePath, _ = r["filePath"].(string)
	f.FilePath = strings.TrimPrefix(f.FilePath, "/")
	f.IsDirectory, _ = r["isDirectory"].(bool)
	f.ContentType, _ = r["contentType"].(string)
	f.LastUpdated, _ = r["lastUpdated"].(string)
	if b, ok := r["archiveBucket"].(map[string]any); ok {
		f.ArchiveID = int64Of(b["id"])
		f.Archive, _ = b["name"].(string)
	}
	return f
}

// archivePath is the files URL of a path in an archive. Each part of the path is escaped.
func archivePath(archive, p string) string {
	if p = strings.Trim(p, "/"); p == "." {
		p = "" // path.Dir of a file at the top
	}
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return "/api/archives/buckets/" + url.PathEscape(archive) + "/files/" + strings.Join(parts, "/")
}

// GetArchiveFile reads one file by its ID. A missing one is IsNotFound.
func (c *Client) GetArchiveFile(ctx context.Context, id int64) (*ArchiveFile, error) {
	p := fmt.Sprintf("/api/archives/files/%d", id)
	var resp struct {
		ArchiveFile Record `json:"archiveFile"`
	}
	if err := c.get(ctx, p, nil, &resp); err != nil {
		return nil, err
	}
	if resp.ArchiveFile == nil {
		return nil, &APIError{Method: http.MethodGet, Path: p, StatusCode: http.StatusNotFound}
	}
	return archiveFileFrom(resp.ArchiveFile), nil
}

// ListArchiveFiles lists the files and folders in one folder of an archive ("" or "/" for the
// top), or every file under it with recursive. A folder that doesn't exist has no files.
func (c *Client) ListArchiveFiles(ctx context.Context, archive, folder string, recursive bool) ([]*ArchiveFile, error) {
	q := url.Values{}
	if recursive {
		q.Set("fullTree", "true")
	}
	all, err := c.ListRecords(ctx, archivePath(archive, folder), "archiveFiles", q)
	if err != nil {
		return nil, err
	}
	out := make([]*ArchiveFile, len(all))
	for i, r := range all {
		out[i] = archiveFileFrom(r)
	}
	return out, nil
}

// FindArchiveFile returns the file at this path in an archive, or nil.
func (c *Client) FindArchiveFile(ctx context.Context, archive, filePath string) (*ArchiveFile, error) {
	filePath = strings.Trim(filePath, "/")
	all, err := c.ListArchiveFiles(ctx, archive, path.Dir(filePath), false)
	if err != nil {
		return nil, err
	}
	for _, f := range all {
		if f.FilePath == filePath && !f.IsDirectory {
			return f, nil
		}
	}
	return nil, nil
}

// UploadArchiveFile writes content to a path in an archive, making its folders on the way. A file
// already at that path is overwritten and keeps its ID.
func (c *Client) UploadArchiveFile(ctx context.Context, archive, filePath string, content []byte) (*ArchiveFile, error) {
	filePath = strings.Trim(filePath, "/")
	q := url.Values{"filename": {path.Base(filePath)}}
	if content == nil {
		content = []byte{}
	}
	var resp struct {
		ArchiveFile Record `json:"archiveFile"`
	}
	if err := c.doPayload(ctx, http.MethodPost, archivePath(archive, path.Dir(filePath)), q, content, "application/octet-stream", &resp); err != nil {
		return nil, err
	}
	if resp.ArchiveFile != nil && int64Of(resp.ArchiveFile["id"]) != 0 {
		return archiveFileFrom(resp.ArchiveFile), nil
	}
	// The answer named no file: find it by its path.
	f, err := c.FindArchiveFile(ctx, archive, filePath)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, fmt.Errorf("archive upload: %s isn't in archive %q after the upload", filePath, archive)
	}
	return f, nil
}

// DeleteArchiveFile deletes a file, or a folder with everything in it. One already gone is not an error.
func (c *Client) DeleteArchiveFile(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/archives/files/%d", id), nil, nil, nil))
}

// ArchiveFileLink is a download link to an archive file. Anyone with its secret can download the
// file, without logging in.
type ArchiveFileLink struct {
	ID             int64
	FileID         int64
	Secret         string
	DateCreated    string
	ExpirationDate string // empty when it never expires
	DownloadCount  int64
}

// ListArchiveFileLinks returns a file's links. A missing file is IsNotFound.
func (c *Client) ListArchiveFileLinks(ctx context.Context, fileID int64) ([]*ArchiveFileLink, error) {
	all, err := c.ListRecords(ctx, fmt.Sprintf("/api/archives/files/%d/links", fileID), "archiveFileLinks", nil)
	if err != nil {
		return nil, err
	}
	out := make([]*ArchiveFileLink, len(all))
	for i, r := range all {
		l := &ArchiveFileLink{ID: int64Of(r["id"]), FileID: fileID, DownloadCount: int64Of(r["downloadCount"])}
		l.Secret, _ = r["secretAccessKey"].(string)
		l.DateCreated, _ = r["dateCreated"].(string)
		l.ExpirationDate, _ = r["expirationDate"].(string)
		out[i] = l
	}
	return out, nil
}

// CreateArchiveFileLink makes a download link that expires after expireSeconds (0: never). The
// answer holds only the secret, so the link is found in the file's links by it. When the answer is
// lost, the one link that wasn't there before is the new one: a public link must never be left
// behind untracked.
func (c *Client) CreateArchiveFileLink(ctx context.Context, fileID, expireSeconds int64) (*ArchiveFileLink, error) {
	before, err := c.ListArchiveFileLinks(ctx, fileID)
	if err != nil {
		return nil, err
	}
	var q url.Values
	if expireSeconds > 0 { // without it the link never expires (live)
		q = url.Values{"expireSeconds": {strconv.FormatInt(expireSeconds, 10)}}
	}
	var resp struct {
		Secret string `json:"secretAccessKey"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/archives/files/%d/links", fileID), q, nil, &resp); err != nil {
		if !MayHaveSucceeded(err) {
			return nil, err
		}
		after, lerr := c.ListArchiveFileLinks(ctx, fileID)
		if lerr != nil {
			return nil, fmt.Errorf("%w (and the file's links could not be read to find a link it may have made: %w)", err, lerr)
		}
		var made []*ArchiveFileLink
		for _, l := range after {
			if !linkIn(before, l.ID) {
				made = append(made, l)
			}
		}
		switch len(made) {
		case 0:
			return nil, err
		case 1:
			return made[0], nil
		}
		return nil, fmt.Errorf("%w; file %d now has %d new links, so the one made here can't be told apart: delete the extra ones in Tools > Archives", err, fileID, len(made))
	}
	if resp.Secret == "" {
		return nil, fmt.Errorf("archive file link: the answer holds no secret")
	}
	links, err := c.ListArchiveFileLinks(ctx, fileID)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.Secret == resp.Secret {
			return l, nil
		}
	}
	return nil, fmt.Errorf("archive file link: the new link isn't in file %d's links", fileID)
}

func linkIn(links []*ArchiveFileLink, id int64) bool {
	for _, l := range links {
		if l.ID == id {
			return true
		}
	}
	return false
}

// DeleteArchiveFileLink deletes a link, so it no longer works. One already gone is not an error.
func (c *Client) DeleteArchiveFileLink(ctx context.Context, fileID, linkID int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/archives/files/%d/links/%d", fileID, linkID), nil, nil, nil))
}

// ArchiveLinkURL is the public download address of a link's secret. It is not under /api: the API
// docs name /api/public-archives/link, which asks for a login on MTN Cloud (live, 2026-10-29).
func (c *Client) ArchiveLinkURL(secret string) string {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/public-archives/link"
	u.RawQuery = url.Values{"s": {secret}}.Encode()
	return u.String()
}

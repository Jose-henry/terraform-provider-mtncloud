package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// A MOS bucket is two things: the bucket in MOS, and its record under Infrastructure > Storage >
// Buckets, which is how customers see it in the console and how MTN's key rotation
// (RevalidateMOS.sh) finds it. Live findings (dev-docs/api-research.md, 2026-10-29) decide the order:
//   - The console's own "Create Bucket" creates the bucket, then checks it on a node that hasn't
//     seen it yet and refuses (NoSuchBucket), leaving it orphaned in MOS. So the provider creates
//     the bucket in MOS, waits until every node sees it, then registers it with createBucket off.
//   - Deleting the record deletes any archive built on it, without warning, and never deletes the
//     bucket in MOS. So destroy refuses while an archive uses it, and deletes the MOS bucket itself.

// StorageBucket is a console bucket record. Its keys are never read into it.
type StorageBucket struct {
	ID           int64
	Name         string // the console name
	BucketName   string // the bucket in MOS
	ProviderType string
	Endpoint     string
	PathStyle    bool
	Active       bool
}

func storageBucketFrom(r Record) *StorageBucket {
	b := &StorageBucket{ID: int64Of(r["id"])}
	b.Name, _ = r["name"].(string)
	b.BucketName, _ = r["bucketName"].(string)
	b.ProviderType, _ = r["providerType"].(string)
	b.Active, _ = r["active"].(bool)
	if cfg, ok := r["config"].(map[string]any); ok {
		b.Endpoint, _ = cfg["endpoint"].(string)
		switch v := cfg["pathStyleAccess"].(type) {
		case bool:
			b.PathStyle = v
		case string:
			b.PathStyle = v == "on" || v == "true"
		}
	}
	return b
}

func int64Of(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

// GetStorageBucket reads one console bucket record.
func (c *Client) GetStorageBucket(ctx context.Context, id int64) (*StorageBucket, error) {
	var resp struct {
		StorageBucket Record `json:"storageBucket"`
	}
	if err := c.get(ctx, fmt.Sprintf("/api/storage-buckets/%d", id), nil, &resp); err != nil {
		return nil, err
	}
	if resp.StorageBucket == nil {
		return nil, &APIError{Method: http.MethodGet, Path: fmt.Sprintf("/api/storage-buckets/%d", id), StatusCode: http.StatusNotFound}
	}
	return storageBucketFrom(resp.StorageBucket), nil
}

// FindStorageBuckets returns the MOS (s3) records for a bucket in MOS, by its bucket name. It lists
// every record and matches bucketName here: which fields the console's phrase search covers isn't
// documented, and a record's console name may differ from its bucket name.
func (c *Client) FindStorageBuckets(ctx context.Context, bucketName string) ([]*StorageBucket, error) {
	all, err := c.ListStorageBuckets(ctx)
	if err != nil {
		return nil, err
	}
	var out []*StorageBucket
	for _, b := range all {
		if b.BucketName == bucketName {
			out = append(out, b)
		}
	}
	return out, nil
}

// ListStorageBuckets returns every MOS (s3) console bucket record, in one list call.
func (c *Client) ListStorageBuckets(ctx context.Context) ([]*StorageBucket, error) {
	all, err := c.ListRecords(ctx, "/api/storage-buckets", "storageBuckets", nil)
	if err != nil {
		return nil, err
	}
	var out []*StorageBucket
	for _, r := range all {
		if b := storageBucketFrom(r); b.ProviderType == "s3" {
			out = append(out, b)
		}
	}
	return out, nil
}

// StorageBucketInput registers a MOS bucket in the console.
type StorageBucketInput struct {
	Name       string // console name
	BucketName string
	Endpoint   string
}

func (c *Client) storageBucketBody(ctx context.Context, in StorageBucketInput, create bool) (map[string]any, error) {
	cr, err := c.MOSCredentials(ctx)
	if err != nil {
		return nil, err
	}
	// The shape of the CLI's dry run (storage-buckets add ... -d), as MTN's customer email describes.
	sb := map[string]any{
		"name": in.Name, "providerType": "s3",
		"config": map[string]any{"accessKey": cr.AccessKey, "secretKey": cr.SecretKey, "endpoint": in.Endpoint, "pathStyleAccess": true},
	}
	if create {
		sb["bucketName"] = in.BucketName
		sb["createBucket"] = false // the bucket exists already: see the top of this file
		sb["copyToStore"] = false
		sb["defaultBackupTarget"] = false
		sb["defaultDeploymentTarget"] = false
		sb["defaultVirtualImageTarget"] = false
	}
	return map[string]any{"storageBucket": sb}, nil
}

// CreateStorageBucket registers an existing MOS bucket in the console and returns the record's ID.
func (c *Client) CreateStorageBucket(ctx context.Context, in StorageBucketInput) (int64, error) {
	body, err := c.storageBucketBody(ctx, in, true)
	if err != nil {
		return 0, err
	}
	var resp struct {
		StorageBucket Record `json:"storageBucket"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/storage-buckets", nil, body, &resp); err != nil {
		return 0, err
	}
	id := int64Of(resp.StorageBucket["id"])
	if id == 0 {
		return 0, errors.New("MTN Cloud API POST /api/storage-buckets: no bucket ID in the answer")
	}
	return id, nil
}

// UpdateStorageBucket changes a record's console name or endpoint. The keys are sent again, as
// MTN's RevalidateMOS.sh does, because the record needs them with every change.
func (c *Client) UpdateStorageBucket(ctx context.Context, id int64, in StorageBucketInput) error {
	body, err := c.storageBucketBody(ctx, in, false)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/storage-buckets/%d", id), nil, body, nil)
}

// DeleteStorageBucket removes the console record only; it never deletes the bucket in MOS. One
// already gone is not an error.
func (c *Client) DeleteStorageBucket(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/storage-buckets/%d", id), nil, nil, nil))
}

// ArchivesOn names the archives built on a console bucket record: deleting the record deletes them.
func (c *Client) ArchivesOn(ctx context.Context, storageBucketID int64) ([]string, error) {
	all, err := c.ListRecords(ctx, "/api/archives/buckets", "archiveBuckets", nil)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, r := range all {
		sp, _ := r["storageProvider"].(map[string]any)
		if int64Of(sp["id"]) == storageBucketID {
			name, _ := r["name"].(string)
			names = append(names, name)
		}
	}
	return names, nil
}

// --- the bucket in MOS

// MOSBucket is a bucket as MOS lists it.
type MOSBucket struct {
	Name         string
	CreationDate string // RFC 3339
}

// ListMOSBuckets lists the tenant's buckets in MOS.
func (c *Client) ListMOSBuckets(ctx context.Context) ([]MOSBucket, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return nil, err
	}
	out := []MOSBucket{}
	p := s3.NewListBucketsPaginator(s, &s3.ListBucketsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, mosErr("list", "buckets", err)
		}
		for _, b := range page.Buckets {
			mb := MOSBucket{Name: aws.ToString(b.Name)}
			if b.CreationDate != nil {
				mb.CreationDate = b.CreationDate.UTC().Format("2006-01-02T15:04:05Z")
			}
			out = append(out, mb)
		}
	}
	return out, nil
}

// MOSBucketExists reports whether a bucket exists (one read; see settle for a confirmed answer).
func (c *Client) MOSBucketExists(ctx context.Context, name string) (bool, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return false, err
	}
	_, err = s.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(name)})
	if err = mosErr("read", "bucket "+name, err); IsMOSNotFound(err) {
		return false, nil
	}
	if me := (*MOSError)(nil); errors.As(err, &me) && (me.Code == "AccessDenied" || me.Status == http.StatusForbidden) {
		return false, c.takenOrDenied(ctx, name, err)
	}
	return err == nil, err
}

// takenOrDenied explains a 403 on a bucket: bucket names are shared by every tenant, so a name
// another tenant owns answers 403, not 404. A bucket the tenant's own list holds keeps the
// access-denied error (the keys may have been replaced).
func (c *Client) takenOrDenied(ctx context.Context, name string, denied error) error {
	own, err := c.ListMOSBuckets(ctx)
	if err != nil {
		return denied
	}
	for _, b := range own {
		if b.Name == name {
			return denied
		}
	}
	return &MOSError{Op: "read", Code: "BucketAlreadyExists", Status: http.StatusForbidden, Msg: fmt.Sprintf(
		"MOS bucket %s: the name is taken by another tenant. Bucket names are shared by every MTN Cloud tenant; choose another, "+
			"for example with your company or project as a prefix.", name)}
}

// WaitMOSBucket waits until every node agrees the bucket exists (present) or is gone.
func (c *Client) WaitMOSBucket(ctx context.Context, name string, present bool) error {
	return c.settle(ctx, func(ctx context.Context) (bool, error) {
		ok, err := c.MOSBucketExists(ctx, name)
		if err != nil {
			return false, err
		}
		if ok != present {
			return false, fmt.Errorf("bucket %s %s", name, map[bool]string{true: "not visible yet", false: "still visible"}[present])
		}
		return true, nil
	})
}

// CreateMOSBucket creates a bucket in MOS and waits until every node sees it. A bucket the tenant
// already owns (a retry after a lost answer) counts as created (rule 11). objectLock creates it
// with object lock on, which can never be turned off.
func (c *Client) CreateMOSBucket(ctx context.Context, name string, objectLock bool) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	in := &s3.CreateBucketInput{Bucket: aws.String(name)}
	if objectLock {
		in.ObjectLockEnabledForBucket = aws.Bool(true)
	}
	_, err = s.CreateBucket(ctx, in)
	if err = mosErr("create", "bucket "+name, err); err != nil && !IsMOSCode(err, "BucketAlreadyOwnedByYou") {
		if IsMOSCode(err, "BucketAlreadyExists") {
			return &MOSError{Op: "create", Code: "BucketAlreadyExists", Msg: fmt.Sprintf(
				"MOS create bucket %s: the name is taken. Bucket names are shared by every tenant; choose another.", name)}
		}
		return err
	}
	return c.WaitMOSBucket(ctx, name, true)
}

// EmptyMOSBucket deletes every object and aborts every unfinished upload in a bucket.
func (c *Client) EmptyMOSBucket(ctx context.Context, name string) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	// MOS keeps no versions (versioning is 501), so the object list is everything.
	p := s3.NewListObjectsV2Paginator(s, &s3.ListObjectsV2Input{Bucket: aws.String(name)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			if err = mosErr("list", "objects in "+name, err); IsMOSNotFound(err) {
				return nil
			}
			return err
		}
		if len(page.Contents) == 0 {
			continue
		}
		ids := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, o := range page.Contents {
			ids = append(ids, types.ObjectIdentifier{Key: o.Key})
		}
		out, err := s.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(name), Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)}})
		if err != nil {
			return mosErr("empty", "bucket "+name, err)
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			return &MOSError{Op: "empty", Code: aws.ToString(e.Code), Msg: fmt.Sprintf(
				"MOS empty bucket %s: %d objects couldn't be deleted, for example %s: %s. An object under retention can't be deleted until it ends.",
				name, len(out.Errors), aws.ToString(e.Key), aws.ToString(e.Message))}
		}
	}
	up := s3.NewListMultipartUploadsPaginator(s, &s3.ListMultipartUploadsInput{Bucket: aws.String(name)})
	for up.HasMorePages() {
		page, err := up.NextPage(ctx)
		if err != nil {
			return mosErr("list", "uploads in "+name, err)
		}
		for _, u := range page.Uploads {
			_, err := s.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(name), Key: u.Key, UploadId: u.UploadId})
			if err = mosErr("abort", "upload in "+name, err); err != nil && !IsMOSNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// DeleteMOSBucket deletes an empty bucket and waits until every node agrees it is gone. A bucket
// already gone is not an error.
func (c *Client) DeleteMOSBucket(ctx context.Context, name string) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	_, err = s.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)})
	if err = mosErr("delete", "bucket "+name, err); err != nil && !IsMOSNotFound(err) {
		return err
	}
	return c.WaitMOSBucket(ctx, name, false)
}

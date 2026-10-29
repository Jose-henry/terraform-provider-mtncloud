package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// MOSObject is an object's metadata, as a HEAD returns it. MOS doesn't keep the content type an
// upload sets (every object reads back as binary/octet-stream), so it isn't here.
type MOSObject struct {
	Bucket, Key  string
	ETag         string // quotes removed; the MD5 of the content for a single-part upload
	Size         int64
	LastModified string // RFC 3339
	Metadata     map[string]string
	LockMode     string // GOVERNANCE, or empty
	RetainUntil  string // RFC 3339, or empty
}

// MOSObjectInput uploads one object.
type MOSObjectInput struct {
	Bucket, Key       string
	Body              []byte
	Metadata          map[string]string
	ChecksumAlgorithm string // CRC32, CRC32C, SHA1 or SHA256; empty sends none
	LockMode          string // GOVERNANCE, with RetainUntil
	RetainUntil       time.Time
	ACL               string // a canned ACL to upload with; empty is private
}

// PutMOSObject uploads an object and waits until every node returns its ETag.
func (c *Client) PutMOSObject(ctx context.Context, in MOSObjectInput) (*MOSObject, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return nil, err
	}
	what := in.Bucket + "/" + in.Key
	req := &s3.PutObjectInput{Bucket: aws.String(in.Bucket), Key: aws.String(in.Key), Body: bytes.NewReader(in.Body), Metadata: in.Metadata}
	if in.ChecksumAlgorithm != "" {
		req.ChecksumAlgorithm = types.ChecksumAlgorithm(in.ChecksumAlgorithm)
	}
	if in.ACL != "" {
		req.ACL = types.ObjectCannedACL(in.ACL)
	}
	if in.LockMode != "" {
		req.ObjectLockMode = types.ObjectLockMode(in.LockMode)
		req.ObjectLockRetainUntilDate = aws.Time(in.RetainUntil)
	}
	out, err := s.PutObject(ctx, req)
	if err != nil {
		return nil, mosErr("upload", what, err)
	}
	etag := strings.Trim(aws.ToString(out.ETag), `"`)
	var got *MOSObject
	err = c.settleN(ctx, mosObjectSettle, func(ctx context.Context) (bool, error) {
		o, err := c.HeadMOSObject(ctx, in.Bucket, in.Key)
		if err != nil {
			return false, err
		}
		got = o
		if etag != "" && o.ETag != etag {
			return false, fmt.Errorf("%s: the new content isn't visible on every node yet", what)
		}
		return true, nil
	})
	return got, err
}

// HeadMOSObject reads an object's metadata.
func (c *Client) HeadMOSObject(ctx context.Context, bucket, key string) (*MOSObject, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, mosErr("read", bucket+"/"+key, err)
	}
	o := &MOSObject{
		Bucket: bucket, Key: key, ETag: strings.Trim(aws.ToString(out.ETag), `"`), Size: aws.ToInt64(out.ContentLength),
		Metadata: map[string]string{}, LockMode: string(out.ObjectLockMode),
	}
	maps.Copy(o.Metadata, out.Metadata)
	if out.LastModified != nil {
		o.LastModified = out.LastModified.UTC().Format(time.RFC3339)
	}
	if out.ObjectLockRetainUntilDate != nil {
		o.RetainUntil = out.ObjectLockRetainUntilDate.UTC().Format(time.RFC3339)
	}
	return o, nil
}

// maxMOSRead caps how much of an object a read returns into Terraform state.
const maxMOSRead = 1 << 20

// ReadMOSObject returns an object's content, up to maxMOSRead bytes; larger is an error.
func (c *Client) ReadMOSObject(ctx context.Context, bucket, key string) ([]byte, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, mosErr("read", bucket+"/"+key, err)
	}
	defer func() { _ = out.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(out.Body, maxMOSRead+1))
	if err != nil {
		return nil, fmt.Errorf("MOS read %s/%s: %w", bucket, key, err)
	}
	if len(b) > maxMOSRead {
		return nil, fmt.Errorf("MOS read %s/%s: the object is larger than 1 MiB, too large to read into Terraform", bucket, key)
	}
	return b, nil
}

// DeleteMOSObject deletes an object. bypassGovernance deletes one under GOVERNANCE retention. One
// already gone is not an error.
func (c *Client) DeleteMOSObject(ctx context.Context, bucket, key string, bypassGovernance bool) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	in := &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if bypassGovernance {
		in.BypassGovernanceRetention = aws.Bool(true)
	}
	_, err = s.DeleteObject(ctx, in)
	if err = mosErr("delete", bucket+"/"+key, err); err != nil && !IsMOSNotFound(err) {
		return err
	}
	return c.settleN(ctx, mosObjectSettle, func(ctx context.Context) (bool, error) {
		_, err := c.HeadMOSObject(ctx, bucket, key)
		if IsMOSNotFound(err) {
			return true, nil
		}
		if err == nil {
			err = fmt.Errorf("%s/%s still visible", bucket, key)
		}
		return false, err
	})
}

// MOSListing is one listing of a bucket: objects, and the common prefixes ("folders") when a
// delimiter was given.
type MOSListing struct {
	Objects  []MOSObject // Key, ETag, Size, LastModified only
	Prefixes []string
}

// ListMOSObjects lists a bucket's objects under prefix, up to max (0 for all).
func (c *Client) ListMOSObjects(ctx context.Context, bucket, prefix, delimiter string, limit int) (*MOSListing, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return nil, err
	}
	in := &s3.ListObjectsV2Input{Bucket: aws.String(bucket)}
	if prefix != "" {
		in.Prefix = aws.String(prefix)
	}
	if delimiter != "" {
		in.Delimiter = aws.String(delimiter)
	}
	out := &MOSListing{Objects: []MOSObject{}, Prefixes: []string{}}
	p := s3.NewListObjectsV2Paginator(s, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, mosErr("list", "objects in "+bucket, err)
		}
		for _, cp := range page.CommonPrefixes { // before the limit can end the listing mid-page
			out.Prefixes = append(out.Prefixes, aws.ToString(cp.Prefix))
		}
		for _, o := range page.Contents {
			mo := MOSObject{Bucket: bucket, Key: aws.ToString(o.Key), ETag: strings.Trim(aws.ToString(o.ETag), `"`), Size: aws.ToInt64(o.Size)}
			if o.LastModified != nil {
				mo.LastModified = o.LastModified.UTC().Format(time.RFC3339)
			}
			out.Objects = append(out.Objects, mo)
			if limit > 0 && len(out.Objects) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

// PutObjectRetention sets an object's GOVERNANCE retention date. Moving it earlier needs bypass.
func (c *Client) PutObjectRetention(ctx context.Context, bucket, key, mode string, until time.Time, bypass bool) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	in := &s3.PutObjectRetentionInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Retention: &types.ObjectLockRetention{Mode: types.ObjectLockRetentionMode(mode), RetainUntilDate: aws.Time(until)},
	}
	if bypass {
		in.BypassGovernanceRetention = aws.Bool(true)
	}
	_, err = s.PutObjectRetention(ctx, in)
	if err != nil {
		return mosErr("set", "the retention of "+bucket+"/"+key, err)
	}
	want := until.UTC().Format(time.RFC3339)
	return c.settle(ctx, func(ctx context.Context) (bool, error) {
		o, err := c.HeadMOSObject(ctx, bucket, key)
		if err != nil {
			return false, err
		}
		if o.RetainUntil != want {
			return false, fmt.Errorf("%s/%s: the retention isn't visible on every node yet", bucket, key)
		}
		return true, nil
	})
}

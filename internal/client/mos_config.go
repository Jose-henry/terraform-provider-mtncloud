package client

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Bucket settings. Every Put waits until reads agree with what was written (settle), because a
// read straight after a write can come from a node that hasn't seen it yet. Every Get returns a
// normalised value (sorted, defaults filled), so equal settings compare equal.

// mosSettingTries bounds how often a setting is written again after it was lost (mosLock).
const mosSettingTries = 3

// mosLock serializes settings writes to one bucket. MOS keeps a bucket's settings together and
// rewrites them all on every PUT, so two writes at once lose one (seen live 2026-10-29: CORS and
// lifecycle put together kept only one; one after the other kept both). Terraform applies a
// bucket's ACL, CORS, lifecycle, logging and object lock in parallel, so they take turns here.
func (c *Client) mosLock(ctx context.Context, key string) (func(), error) {
	v, _ := c.mosLocks.LoadOrStore(key, make(chan struct{}, 1))
	ch := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for another change to %s: %w", key, ctx.Err())
	}
}

// putThenSettle writes, then reads until the read equals want mosSettle times in a row. lockKey
// names what the setting belongs to (the bucket). A write that never shows up was lost to another
// writer of the same bucket (outside this run), so it is written again, up to mosSettingTries times.
func putThenSettle[T any](ctx context.Context, c *Client, lockKey, what string, put func(context.Context) error, get func(context.Context) (T, error), want T) error {
	unlock, err := c.mosLock(ctx, lockKey)
	if err != nil {
		return err
	}
	defer unlock()
	for try := 1; ; try++ {
		if err := put(ctx); err != nil {
			return err
		}
		sctx, cancel := context.WithTimeout(ctx, c.cfg.MOSLostAfter)
		err := c.settle(sctx, func(ctx context.Context) (bool, error) {
			got, err := get(ctx)
			if err != nil {
				return false, err
			}
			if !reflect.DeepEqual(got, want) {
				return false, fmt.Errorf("%s not visible on every node yet", what)
			}
			return true, nil
		})
		cancel()
		if err == nil || ctx.Err() != nil || try == mosSettingTries || !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		c.log(ctx, "MOS setting not visible after a write; writing it again", map[string]any{"what": what, "bucket": lockKey, "try": try})
	}
}

// --- ACLs

// CannedACLs lists the canned ACLs a bucket or object can be set to. "custom" is what a read
// reports for grants that match none of them.
func CannedACLs() []string {
	return []string{"private", "public-read", "public-read-write", "authenticated-read"}
}

const (
	allUsers  = "http://acs.amazonaws.com/groups/global/AllUsers"
	authUsers = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers"
)

// ACLGrant is one entry of an ACL, as a string such as "AllUsers:READ" or "owner:FULL_CONTROL".
type ACLGrant = string

// cannedOf names the canned ACL these grants are, or "custom".
func cannedOf(owner string, grants []types.Grant) (string, []ACLGrant) {
	var list []ACLGrant
	for _, g := range grants {
		who := "?"
		if g.Grantee != nil {
			switch {
			case aws.ToString(g.Grantee.URI) == allUsers:
				who = "AllUsers"
			case aws.ToString(g.Grantee.URI) == authUsers:
				who = "AuthenticatedUsers"
			case aws.ToString(g.Grantee.ID) == owner:
				who = "owner"
			case g.Grantee.ID != nil:
				who = "user:" + aws.ToString(g.Grantee.ID)
			case g.Grantee.URI != nil:
				who = aws.ToString(g.Grantee.URI)
			}
		}
		list = append(list, who+":"+string(g.Permission))
	}
	slices.Sort(list)
	list = slices.Compact(list)
	canned := map[string][]string{
		"private":            {"owner:FULL_CONTROL"},
		"public-read":        {"AllUsers:READ", "owner:FULL_CONTROL"},
		"public-read-write":  {"AllUsers:READ", "AllUsers:WRITE", "owner:FULL_CONTROL"},
		"authenticated-read": {"AuthenticatedUsers:READ", "owner:FULL_CONTROL"},
	}
	for name, want := range canned {
		if slices.Equal(list, want) {
			return name, list
		}
	}
	return "custom", list
}

// ACL is a bucket's or object's access, as the canned ACL it matches and its grants.
type ACL struct {
	Canned string
	Grants []ACLGrant
}

// GetBucketACL reads a bucket's ACL.
func (c *Client) GetBucketACL(ctx context.Context, bucket string) (ACL, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return ACL{}, err
	}
	out, err := s.GetBucketAcl(ctx, &s3.GetBucketAclInput{Bucket: aws.String(bucket)})
	if err != nil {
		return ACL{}, mosErr("read", "the ACL of bucket "+bucket, err)
	}
	owner := ""
	if out.Owner != nil {
		owner = aws.ToString(out.Owner.ID)
	}
	canned, grants := cannedOf(owner, out.Grants)
	return ACL{Canned: canned, Grants: grants}, nil
}

// PutBucketACL sets a bucket's canned ACL.
func (c *Client) PutBucketACL(ctx context.Context, bucket, canned string) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	put := func(ctx context.Context) error {
		_, err := s.PutBucketAcl(ctx, &s3.PutBucketAclInput{Bucket: aws.String(bucket), ACL: types.BucketCannedACL(canned)})
		return mosErr("set", "the ACL of bucket "+bucket, err)
	}
	get := func(ctx context.Context) (string, error) {
		a, err := c.GetBucketACL(ctx, bucket)
		return a.Canned, err
	}
	return putThenSettle(ctx, c, bucket, "bucket ACL", put, get, canned)
}

// GetObjectACL reads an object's ACL.
func (c *Client) GetObjectACL(ctx context.Context, bucket, key string) (ACL, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return ACL{}, err
	}
	out, err := s.GetObjectAcl(ctx, &s3.GetObjectAclInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return ACL{}, mosErr("read", "the ACL of "+bucket+"/"+key, err)
	}
	owner := ""
	if out.Owner != nil {
		owner = aws.ToString(out.Owner.ID)
	}
	canned, grants := cannedOf(owner, out.Grants)
	return ACL{Canned: canned, Grants: grants}, nil
}

// PutObjectACL sets an object's canned ACL.
func (c *Client) PutObjectACL(ctx context.Context, bucket, key, canned string) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	put := func(ctx context.Context) error {
		_, err := s.PutObjectAcl(ctx, &s3.PutObjectAclInput{Bucket: aws.String(bucket), Key: aws.String(key), ACL: types.ObjectCannedACL(canned)})
		return mosErr("set", "the ACL of "+bucket+"/"+key, err)
	}
	get := func(ctx context.Context) (string, error) {
		a, err := c.GetObjectACL(ctx, bucket, key)
		return a.Canned, err
	}
	return putThenSettle(ctx, c, bucket+"/"+key, "object ACL", put, get, canned)
}

// --- CORS

// CORSRule is one CORS rule. Lists are sorted; methods are upper case.
type CORSRule struct {
	ID             string
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposeHeaders  []string
	MaxAgeSeconds  int64
}

func normCORS(in []CORSRule) []CORSRule {
	out := make([]CORSRule, 0, len(in))
	for _, r := range in {
		r.AllowedOrigins = sortedCopy(r.AllowedOrigins)
		r.AllowedHeaders = sortedCopy(r.AllowedHeaders)
		r.ExposeHeaders = sortedCopy(r.ExposeHeaders)
		m := make([]string, len(r.AllowedMethods))
		for i, x := range r.AllowedMethods {
			m[i] = strings.ToUpper(x)
		}
		r.AllowedMethods = sortedCopy(m)
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b CORSRule) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	slices.Sort(out)
	return out
}

// GetBucketCORS reads a bucket's CORS rules; none is an empty list.
func (c *Client) GetBucketCORS(ctx context.Context, bucket string) ([]CORSRule, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(bucket)})
	if err = mosErr("read", "the CORS rules of bucket "+bucket, err); IsMOSCode(err, "NoSuchCORSConfiguration") {
		return []CORSRule{}, nil
	} else if err != nil {
		return nil, err
	}
	rules := make([]CORSRule, 0, len(out.CORSRules))
	for _, r := range out.CORSRules {
		rules = append(rules, CORSRule{
			ID: aws.ToString(r.ID), AllowedOrigins: r.AllowedOrigins, AllowedMethods: r.AllowedMethods,
			AllowedHeaders: r.AllowedHeaders, ExposeHeaders: r.ExposeHeaders, MaxAgeSeconds: int64(aws.ToInt32(r.MaxAgeSeconds)),
		})
	}
	return normCORS(rules), nil
}

// PutBucketCORS replaces a bucket's CORS rules; an empty list removes them.
func (c *Client) PutBucketCORS(ctx context.Context, bucket string, rules []CORSRule) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	put := func(ctx context.Context) error {
		if len(rules) == 0 {
			_, err := s.DeleteBucketCors(ctx, &s3.DeleteBucketCorsInput{Bucket: aws.String(bucket)})
			return mosErr("remove", "the CORS rules of bucket "+bucket, err)
		}
		in := make([]types.CORSRule, 0, len(rules))
		for _, r := range rules {
			cr := types.CORSRule{AllowedOrigins: r.AllowedOrigins, AllowedMethods: r.AllowedMethods, AllowedHeaders: r.AllowedHeaders, ExposeHeaders: r.ExposeHeaders}
			if r.ID != "" {
				cr.ID = aws.String(r.ID)
			}
			if r.MaxAgeSeconds > 0 {
				cr.MaxAgeSeconds = aws.Int32(int32(r.MaxAgeSeconds)) //nolint:gosec // validated 0..86400 at plan time
			}
			in = append(in, cr)
		}
		_, err := s.PutBucketCors(ctx, &s3.PutBucketCorsInput{Bucket: aws.String(bucket), CORSConfiguration: &types.CORSConfiguration{CORSRules: in}})
		return mosErr("set", "the CORS rules of bucket "+bucket, err)
	}
	return putThenSettle(ctx, c, bucket, "CORS rules", put, func(ctx context.Context) ([]CORSRule, error) { return c.GetBucketCORS(ctx, bucket) }, normCORS(rules))
}

// --- lifecycle

// LifecycleRule is one lifecycle rule MOS supports: expiry by prefix (after days or on a date),
// and cleanup of unfinished uploads. Tag filters and transitions answer 501 on MOS.
type LifecycleRule struct {
	ID                        string
	Prefix                    string
	Enabled                   bool
	ExpirationDays            int64
	ExpirationDate            string // YYYY-MM-DD, or empty
	AbortIncompleteUploadDays int64
}

func normLifecycle(in []LifecycleRule) []LifecycleRule {
	out := append([]LifecycleRule{}, in...)
	slices.SortFunc(out, func(a, b LifecycleRule) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// GetBucketLifecycle reads a bucket's lifecycle rules; none is an empty list.
func (c *Client) GetBucketLifecycle(ctx context.Context, bucket string) ([]LifecycleRule, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
	if err = mosErr("read", "the lifecycle rules of bucket "+bucket, err); IsMOSCode(err, "NoSuchLifecycleConfiguration") {
		return []LifecycleRule{}, nil
	} else if err != nil {
		return nil, err
	}
	rules := make([]LifecycleRule, 0, len(out.Rules))
	for _, r := range out.Rules {
		lr := LifecycleRule{ID: aws.ToString(r.ID), Enabled: r.Status == types.ExpirationStatusEnabled}
		if r.Filter != nil && r.Filter.Prefix != nil {
			lr.Prefix = aws.ToString(r.Filter.Prefix)
		} else if r.Prefix != nil { //nolint:staticcheck // MOS may answer with the older top-level prefix
			lr.Prefix = aws.ToString(r.Prefix) //nolint:staticcheck // same
		}
		if e := r.Expiration; e != nil {
			lr.ExpirationDays = int64(aws.ToInt32(e.Days))
			if e.Date != nil {
				lr.ExpirationDate = e.Date.UTC().Format("2006-01-02")
			}
		}
		if a := r.AbortIncompleteMultipartUpload; a != nil {
			lr.AbortIncompleteUploadDays = int64(aws.ToInt32(a.DaysAfterInitiation))
		}
		rules = append(rules, lr)
	}
	return normLifecycle(rules), nil
}

// PutBucketLifecycle replaces a bucket's lifecycle rules; an empty list removes them.
func (c *Client) PutBucketLifecycle(ctx context.Context, bucket string, rules []LifecycleRule) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	put := func(ctx context.Context) error {
		if len(rules) == 0 {
			_, err := s.DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String(bucket)})
			return mosErr("remove", "the lifecycle rules of bucket "+bucket, err)
		}
		in := make([]types.LifecycleRule, 0, len(rules))
		for _, r := range rules {
			lr := types.LifecycleRule{ID: aws.String(r.ID), Status: types.ExpirationStatusDisabled, Filter: &types.LifecycleRuleFilter{Prefix: aws.String(r.Prefix)}}
			if r.Enabled {
				lr.Status = types.ExpirationStatusEnabled
			}
			switch {
			case r.ExpirationDays > 0:
				lr.Expiration = &types.LifecycleExpiration{Days: aws.Int32(int32(r.ExpirationDays))} //nolint:gosec // validated at plan time
			case r.ExpirationDate != "":
				d, err := time.Parse("2006-01-02", r.ExpirationDate)
				if err != nil {
					return fmt.Errorf("lifecycle rule %s: expiration date %q isn't YYYY-MM-DD", r.ID, r.ExpirationDate)
				}
				lr.Expiration = &types.LifecycleExpiration{Date: aws.Time(d)}
			}
			if r.AbortIncompleteUploadDays > 0 {
				lr.AbortIncompleteMultipartUpload = &types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(int32(r.AbortIncompleteUploadDays))} //nolint:gosec // validated
			}
			in = append(in, lr)
		}
		_, err := s.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
			Bucket: aws.String(bucket), LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: in},
		})
		return mosErr("set", "the lifecycle rules of bucket "+bucket, err)
	}
	return putThenSettle(ctx, c, bucket, "lifecycle rules", put, func(ctx context.Context) ([]LifecycleRule, error) { return c.GetBucketLifecycle(ctx, bucket) }, normLifecycle(rules))
}

// --- access logging

// BucketLogging is where a bucket's access logs go; an empty TargetBucket means off.
type BucketLogging struct {
	TargetBucket string
	TargetPrefix string
}

// GetBucketLogging reads a bucket's access logging.
func (c *Client) GetBucketLogging(ctx context.Context, bucket string) (BucketLogging, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return BucketLogging{}, err
	}
	out, err := s.GetBucketLogging(ctx, &s3.GetBucketLoggingInput{Bucket: aws.String(bucket)})
	if err != nil {
		return BucketLogging{}, mosErr("read", "the access logging of bucket "+bucket, err)
	}
	if out.LoggingEnabled == nil {
		return BucketLogging{}, nil
	}
	return BucketLogging{TargetBucket: aws.ToString(out.LoggingEnabled.TargetBucket), TargetPrefix: aws.ToString(out.LoggingEnabled.TargetPrefix)}, nil
}

// PutBucketLogging turns access logging on (to want.TargetBucket) or off (empty target).
func (c *Client) PutBucketLogging(ctx context.Context, bucket string, want BucketLogging) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	put := func(ctx context.Context) error {
		st := &types.BucketLoggingStatus{}
		if want.TargetBucket != "" {
			st.LoggingEnabled = &types.LoggingEnabled{TargetBucket: aws.String(want.TargetBucket), TargetPrefix: aws.String(want.TargetPrefix)}
		}
		_, err := s.PutBucketLogging(ctx, &s3.PutBucketLoggingInput{Bucket: aws.String(bucket), BucketLoggingStatus: st})
		return mosErr("set", "the access logging of bucket "+bucket, err)
	}
	return putThenSettle(ctx, c, bucket, "access logging", put, func(ctx context.Context) (BucketLogging, error) { return c.GetBucketLogging(ctx, bucket) }, want)
}

// --- object lock

// ObjectLockDefault is a bucket's default retention for new objects (GOVERNANCE mode only on
// OneFS). Zero Days and Years means no default.
type ObjectLockDefault struct {
	Enabled bool // object lock is on for the bucket (set when it was created)
	Mode    string
	Days    int64
	Years   int64
}

// GetObjectLock reads a bucket's object lock configuration.
func (c *Client) GetObjectLock(ctx context.Context, bucket string) (ObjectLockDefault, error) {
	s, err := c.mos(ctx)
	if err != nil {
		return ObjectLockDefault{}, err
	}
	out, err := s.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(bucket)})
	if err = mosErr("read", "the object lock of bucket "+bucket, err); IsMOSCode(err, "ObjectLockConfigurationNotFoundError") {
		return ObjectLockDefault{}, nil
	} else if err != nil {
		return ObjectLockDefault{}, err
	}
	var d ObjectLockDefault
	if cfg := out.ObjectLockConfiguration; cfg != nil {
		d.Enabled = cfg.ObjectLockEnabled == types.ObjectLockEnabledEnabled
		if r := cfg.Rule; r != nil && r.DefaultRetention != nil {
			d.Mode = string(r.DefaultRetention.Mode)
			d.Days = int64(aws.ToInt32(r.DefaultRetention.Days))
			d.Years = int64(aws.ToInt32(r.DefaultRetention.Years))
		}
	}
	return d, nil
}

// PutObjectLock sets (or, with zero Days and Years, clears) a bucket's default retention.
func (c *Client) PutObjectLock(ctx context.Context, bucket string, want ObjectLockDefault) error {
	s, err := c.mos(ctx)
	if err != nil {
		return err
	}
	want.Enabled = true
	put := func(ctx context.Context) error {
		cfg := &types.ObjectLockConfiguration{ObjectLockEnabled: types.ObjectLockEnabledEnabled}
		if want.Days > 0 || want.Years > 0 {
			dr := &types.DefaultRetention{Mode: types.ObjectLockRetentionMode(want.Mode)}
			if want.Days > 0 {
				dr.Days = aws.Int32(int32(want.Days)) //nolint:gosec // validated at plan time
			} else {
				dr.Years = aws.Int32(int32(want.Years)) //nolint:gosec // validated at plan time
			}
			cfg.Rule = &types.ObjectLockRule{DefaultRetention: dr}
		}
		_, err := s.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{Bucket: aws.String(bucket), ObjectLockConfiguration: cfg})
		return mosErr("set", "the object lock of bucket "+bucket, err)
	}
	if want.Days == 0 && want.Years == 0 {
		want.Mode = ""
	}
	return putThenSettle(ctx, c, bucket, "object lock", put, func(ctx context.Context) (ObjectLockDefault, error) { return c.GetObjectLock(ctx, bucket) }, want)
}

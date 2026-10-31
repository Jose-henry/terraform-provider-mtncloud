package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// MOS (MTN Object Storage) is OneFS S3 behind https://<tenant>.mos-cloud1.cloud.mtn.ng. Facts that
// shape this file (dev-docs/api-research.md, live probe 2026-10-29):
//   - Keys are one pair per tenant, made by the MOS catalog order and stored in Cypher as
//     secret/MOS-<tenant>_<user>_{access_key,secret_key,public_endpoint_url,private_endpoint_url}.
//     Ordering again rotates them, so the provider only ever reads them (D-007).
//   - Path-style addressing, SigV4, region us-east-1.
//   - A change takes a few seconds to reach every node: reads straight after a write can be stale,
//     so writes are confirmed by reading until several reads in a row agree (settle).
//   - Checksum support is off on the cluster, so the SDK sends checksums only when asked.

// MOSCredentials are the tenant's MOS keys and endpoints. Never log the keys.
type MOSCredentials struct {
	AccessKey       string
	SecretKey       string
	PublicEndpoint  string
	PrivateEndpoint string
	Endpoint        string // the one the provider uses
	Source          string // "cypher" or "environment"
	CypherPrefix    string // secret/MOS-<tenant>_<user>_, when read from Cypher
}

// ErrMOSNotSetUp means the tenant has no MOS keys in Cypher.
var ErrMOSNotSetUp = errors.New("MTN Object Storage isn't set up for this tenant: no MOS keys were found in Cypher. Add " +
	"`resource \"mtncloud_mos_access\" \"keys\" {}` (it orders them once, as the tenant has none) and make MOS resources " +
	"depend on it, or order \"MTN Object Storage (MOS)\" from the catalog once, or set MTNCLOUD_MOS_ACCESS_KEY, " +
	"MTNCLOUD_MOS_SECRET_KEY and MTNCLOUD_MOS_ENDPOINT")

// MOSTenantName is the tenant name as MTN's MOS workflow writes it into Cypher key names:
// newlines dropped, spaces to "_", then every ASCII punctuation character removed (which removes
// those underscores too), then lower case. Same as `tr ' ' '_' | tr -d '[:punct:]' | tr A-Z a-z`.
func MOSTenantName(account string) string {
	var b strings.Builder
	for _, r := range account {
		switch {
		case r == '\n' || r == '\r':
		case r == ' ', r < unicode.MaxASCII && unicode.IsPunct(r), r < unicode.MaxASCII && unicode.IsSymbol(r):
			// [:punct:] in the C locale is every printable ASCII character that isn't a letter, digit or space.
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// MOSCredentials finds the tenant's MOS keys, once per provider instance. The environment wins
// when MTNCLOUD_MOS_ACCESS_KEY is set; MTNCLOUD_MOS_ENDPOINT picks "public" (default), "private" or
// a URL either way.
func (c *Client) MOSCredentials(ctx context.Context) (*MOSCredentials, error) {
	return cached(ctx, &c.cache, "mos-credentials", func(ctx context.Context) (*MOSCredentials, error) {
		want := strings.TrimSpace(os.Getenv("MTNCLOUD_MOS_ENDPOINT"))
		if ak := os.Getenv("MTNCLOUD_MOS_ACCESS_KEY"); ak != "" {
			cr := &MOSCredentials{AccessKey: ak, SecretKey: os.Getenv("MTNCLOUD_MOS_SECRET_KEY"), Source: "environment"}
			if cr.SecretKey == "" {
				return nil, errors.New("MTNCLOUD_MOS_ACCESS_KEY is set but MTNCLOUD_MOS_SECRET_KEY isn't: set both, or neither to use the keys in Cypher")
			}
			if !strings.HasPrefix(want, "https://") {
				return nil, errors.New("with MTNCLOUD_MOS_ACCESS_KEY set, MTNCLOUD_MOS_ENDPOINT must be the endpoint URL (https://...)")
			}
			cr.Endpoint, cr.PublicEndpoint = want, want
			return cr, nil
		}
		me, err := c.WhoAmI(ctx)
		if err != nil {
			return nil, err
		}
		tenant := MOSTenantName(me.AccountName)
		phrase := "secret/MOS-" + tenant + "_"
		recs, err := c.ListRecords(ctx, "/api/cypher", "cyphers", url.Values{"phrase": {phrase}})
		if err != nil {
			return nil, fmt.Errorf("reading the MOS keys from Cypher: %w", err)
		}
		// One key of each kind, as MTN's workflow keeps it: MOS-<tenant>_<user>_access_key.
		prefix := ""
		for _, r := range recs {
			if k, _ := r["itemKey"].(string); strings.HasPrefix(k, phrase) && strings.HasSuffix(k, "_access_key") {
				prefix = strings.TrimSuffix(k, "access_key")
				break
			}
		}
		if prefix == "" {
			return nil, ErrMOSNotSetUp
		}
		cr := &MOSCredentials{Source: "cypher", CypherPrefix: prefix}
		for suffix, dst := range map[string]*string{
			"access_key": &cr.AccessKey, "secret_key": &cr.SecretKey,
			"public_endpoint_url": &cr.PublicEndpoint, "private_endpoint_url": &cr.PrivateEndpoint,
		} {
			s, err := c.ReadCypher(ctx, prefix+suffix)
			if IsNotFound(err) && strings.HasSuffix(suffix, "endpoint_url") {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("reading the MOS key %s%s from Cypher: %w", prefix, suffix, err)
			}
			*dst = strings.TrimSpace(s.Value)
		}
		if cr.AccessKey == "" || cr.SecretKey == "" {
			return nil, ErrMOSNotSetUp
		}
		switch want {
		case "", "public":
			cr.Endpoint = cr.PublicEndpoint
		case "private":
			cr.Endpoint = cr.PrivateEndpoint
		default:
			if !strings.HasPrefix(want, "https://") {
				return nil, fmt.Errorf("MTNCLOUD_MOS_ENDPOINT is %q: use public, private or an https:// URL", want)
			}
			cr.Endpoint = want
		}
		if cr.Endpoint == "" {
			return nil, fmt.Errorf("the tenant's MOS keys have no %s endpoint in Cypher; set MTNCLOUD_MOS_ENDPOINT to the URL", map[bool]string{true: "private", false: "public"}[want == "private"])
		}
		return cr, nil
	})
}

// limitedTransport makes the S3 client wait for the same rate limiter as every console call.
type limitedTransport struct {
	c    *Client
	next http.RoundTripper
}

func (t limitedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := t.c.limiter.Wait(r.Context()); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(r)
}

// mosAttempts caps the SDK's own retries (5xx, 429, throttling, network errors): the only retries
// for MOS calls, with the SDK's exponential backoff and jitter.
const mosAttempts = 5

// mos returns the S3 client for the tenant's MOS, built once per provider instance.
func (c *Client) mos(ctx context.Context) (*s3.Client, error) {
	return cached(ctx, &c.cache, "mos-s3", func(ctx context.Context) (*s3.Client, error) {
		cr, err := c.MOSCredentials(ctx)
		if err != nil {
			return nil, err
		}
		next := c.http.Transport
		if next == nil {
			next = http.DefaultTransport
		}
		hc := &http.Client{Timeout: 5 * time.Minute, Transport: limitedTransport{c: c, next: next}}
		return s3.New(s3.Options{
			Region:                     "us-east-1",
			BaseEndpoint:               aws.String(cr.Endpoint),
			UsePathStyle:               true,
			Credentials:                aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cr.AccessKey, cr.SecretKey, "")),
			HTTPClient:                 hc,
			RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
			ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
			Retryer:                    retry.AddWithMaxAttempts(retry.NewStandard(), mosAttempts),
		}), nil
	})
}

// MOSError is a refusal from MOS, with a message that says what to do.
type MOSError struct {
	Op, Code, Msg string
	Status        int
}

func (e *MOSError) Error() string { return e.Msg }

// mosErr turns an SDK error into a *MOSError, or returns it as it is (network, ctx).
func mosErr(op, what string, err error) error {
	if err == nil {
		return nil
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		return fmt.Errorf("MOS %s %s: %w", op, what, err)
	}
	e := &MOSError{Op: op, Code: ae.ErrorCode()}
	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) {
		e.Status = re.HTTPStatusCode()
	}
	switch {
	case e.Code == "NotImplemented" || e.Status == http.StatusNotImplemented:
		e.Msg = fmt.Sprintf("MOS %s %s: MTN Object Storage doesn't support this.", op, what)
	case strings.Contains(e.Code, "ObjectLock") || strings.Contains(strings.ToLower(ae.ErrorMessage()), "object lock"):
		e.Msg = fmt.Sprintf("MOS %s %s: object lock isn't enabled on MTN Object Storage yet (%s). Ask MTN Cloud support to enable it.", op, what, ae.ErrorCode())
	case e.Code == "BucketNotEmpty":
		e.Msg = fmt.Sprintf("MOS %s %s: the bucket isn't empty. Delete its objects first, or set force_destroy = true.", op, what)
	case e.Code == "AccessDenied" || e.Status == http.StatusForbidden:
		e.Msg = fmt.Sprintf("MOS %s %s: access denied. An object under retention can't be changed or deleted; otherwise the tenant's MOS keys "+
			"may have been replaced by a new MOS order (run again to read the new ones).", op, what)
	default:
		e.Msg = fmt.Sprintf("MOS %s %s: %s (%s)", op, what, ae.ErrorMessage(), ae.ErrorCode())
	}
	return e
}

// IsMOSCode reports a MOS refusal with one of these error codes.
func IsMOSCode(err error, codes ...string) bool {
	var e *MOSError
	if !errors.As(err, &e) {
		return false
	}
	for _, c := range codes {
		if e.Code == c {
			return true
		}
	}
	return false
}

// IsMOSNotFound reports a missing bucket or object (or one the cluster answered 404 for).
func IsMOSNotFound(err error) bool {
	var e *MOSError
	return errors.As(err, &e) && (e.Status == http.StatusNotFound || strings.HasPrefix(e.Code, "NoSuch") || e.Code == "NotFound")
}

// mosSettle is how many reads in a row must agree before a write counts as visible everywhere.
// Objects were consistent at once in the probe (bucket settings took 2 to 10 s), so an object
// write needs fewer: one upload or delete then costs 2 reads, not 6.
const (
	mosSettle       = 6
	mosObjectSettle = 2
)

// settle reads until check holds mosSettle times in a row, or ctx ends (a resource's timeouts
// bound it). It returns the last error when check never held.
func (c *Client) settle(ctx context.Context, check func(context.Context) (bool, error)) error {
	return c.settleN(ctx, mosSettle, check)
}

// settleN is settle with its own count of agreeing reads.
func (c *Client) settleN(ctx context.Context, n int, check func(context.Context) (bool, error)) error {
	ok, wait := 0, c.cfg.PollMin
	var last error
	for ok < n {
		good, err := check(ctx)
		if ctx.Err() != nil {
			if last != nil {
				return fmt.Errorf("%w (last read: %w)", ctx.Err(), last)
			}
			return ctx.Err()
		}
		if good {
			ok++
		} else {
			ok, last = 0, err
		}
		if ok < n {
			if err := sleep(ctx, min(wait, time.Second)); err != nil {
				if last != nil {
					return fmt.Errorf("%w (last read: %w)", err, last)
				}
				return err
			}
			if !good {
				wait = min(wait*2, 4*time.Second)
			}
		}
	}
	return nil
}

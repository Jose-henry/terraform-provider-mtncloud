package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestMOSTenantName(t *testing.T) {
	// The same as MTN's workflow: tr ' ' '_' | tr -d '[:punct:]' | tr A-Z a-z, which also deletes
	// the underscores the spaces became.
	for in, want := range map[string]string{
		"Acme":                "acme",
		"MTN Cloud Demo":      "mtnclouddemo",
		"Kestrel-Dev_01":      "kestreldev01",
		"Café & Co.":          "café co",
		"Line\nBreak":         "linebreak",
		"tabs\tstay":          "tabs\tstay",
		"Bank (Nigeria) Ltd.": "banknigerialtd",
	} {
		if in == "Café & Co." {
			want = "caféco" // the space is removed with the punctuation
		}
		if got := MOSTenantName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func clearMOSEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MTNCLOUD_MOS_ACCESS_KEY", "MTNCLOUD_MOS_SECRET_KEY", "MTNCLOUD_MOS_ENDPOINT"} {
		t.Setenv(k, "")
	}
}

// cypherFake answers whoami for tenant "Acme" and the MOS keys MTN's workflow writes.
func cypherFake(t *testing.T, keys map[string]string) *fakeAPI {
	t.Helper()
	f := newFakeAPI(t)
	f.route("GET", "/api/whoami", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"user":{"account":{"id":202,"name":"Acme"}}}`))
	})
	f.route("GET", "/api/cypher", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("phrase") != "secret/MOS-acme_" {
			t.Errorf("phrase %q", r.URL.Query().Get("phrase"))
		}
		var rows []string
		for k := range keys {
			rows = append(rows, fmt.Sprintf(`{"itemKey":%q}`, k))
		}
		slices.Sort(rows)
		_, _ = fmt.Fprintf(w, `{"cyphers":[%s],"meta":{"total":%d}}`, strings.Join(rows, ","), len(rows))
	})
	for k, v := range keys {
		f.route("GET", "/api/cypher/"+k, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"data":%q,"lease_duration":0}`, v)
		})
	}
	return f
}

func TestMOSCredentials_FromCypher(t *testing.T) {
	clearMOSEnv(t)
	f := cypherFake(t, map[string]string{
		"secret/MOS-acme_jdoe_access_key":           "AK",
		"secret/MOS-acme_jdoe_secret_key":           "SK",
		"secret/MOS-acme_jdoe_public_endpoint_url":  "https://acme.mos.example\n",
		"secret/MOS-acme_jdoe_private_endpoint_url": "https://10.20.0.9:9021",
		"secret/MOS-acmecorp_x_access_key":          "another tenant's",
	})
	c := f.client(t, fastPoll)
	cr, err := c.MOSCredentials(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cr.AccessKey != "AK" || cr.SecretKey != "SK" || cr.Endpoint != "https://acme.mos.example" || cr.PrivateEndpoint != "https://10.20.0.9:9021" || cr.Source != "cypher" {
		t.Errorf("got %+v", cr)
	}
	if _, err := c.MOSCredentials(t.Context()); err != nil || f.count("GET", "/api/cypher") != 1 {
		t.Errorf("keys read %d times, want once per provider", f.count("GET", "/api/cypher"))
	}
}

func TestMOSCredentials_PrivateEndpointByEnv(t *testing.T) {
	clearMOSEnv(t)
	t.Setenv("MTNCLOUD_MOS_ENDPOINT", "private")
	f := cypherFake(t, map[string]string{
		"secret/MOS-acme_jdoe_access_key": "AK", "secret/MOS-acme_jdoe_secret_key": "SK",
		"secret/MOS-acme_jdoe_public_endpoint_url": "https://acme.mos.example",
	})
	_, err := f.client(t, fastPoll).MOSCredentials(t.Context())
	if err == nil || !strings.Contains(err.Error(), "no private endpoint") {
		t.Fatalf("err %v", err)
	}
}

func TestMOSCredentials_NotSetUp(t *testing.T) {
	clearMOSEnv(t)
	f := cypherFake(t, map[string]string{"secret/MOS-acmecorp_x_access_key": "another tenant's"})
	if _, err := f.client(t, fastPoll).MOSCredentials(t.Context()); !errors.Is(err, ErrMOSNotSetUp) {
		t.Fatalf("err %v", err)
	}
}

func TestMOSCredentials_Environment(t *testing.T) {
	for name, c := range map[string]struct {
		ak, sk, ep string
		want       string
	}{
		"no secret":   {"AK", "", "https://x", "MTNCLOUD_MOS_SECRET_KEY isn't"},
		"not a URL":   {"AK", "SK", "public", "must be the endpoint URL"},
		"plain http":  {"AK", "SK", "http://x", "must be the endpoint URL"},
		"all present": {"AK", "SK", "https://x", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MTNCLOUD_MOS_ACCESS_KEY", c.ak)
			t.Setenv("MTNCLOUD_MOS_SECRET_KEY", c.sk)
			t.Setenv("MTNCLOUD_MOS_ENDPOINT", c.ep)
			cr, err := newFakeAPI(t).client(t).MOSCredentials(t.Context())
			switch {
			case c.want == "" && (err != nil || cr.Source != "environment" || cr.Endpoint != "https://x"):
				t.Errorf("got %+v, %v", cr, err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("err %v, want %q", err, c.want)
			}
		})
	}
}

func TestCannedOf(t *testing.T) {
	owner := func(id string) types.Grant {
		return types.Grant{Grantee: &types.Grantee{ID: aws.String(id)}, Permission: types.PermissionFullControl}
	}
	group := func(uri string, p types.Permission) types.Grant {
		return types.Grant{Grantee: &types.Grantee{URI: aws.String(uri)}, Permission: p}
	}
	for name, c := range map[string]struct {
		grants []types.Grant
		want   string
	}{
		"private":            {[]types.Grant{owner("t")}, "private"},
		"owner listed twice": {[]types.Grant{owner("t"), owner("t")}, "private"},
		"public-read":        {[]types.Grant{group(allUsers, types.PermissionRead), owner("t")}, "public-read"},
		"public-read-write":  {[]types.Grant{owner("t"), group(allUsers, types.PermissionWrite), group(allUsers, types.PermissionRead)}, "public-read-write"},
		"authenticated-read": {[]types.Grant{owner("t"), group(authUsers, types.PermissionRead)}, "authenticated-read"},
		"another user":       {[]types.Grant{owner("t"), {Grantee: &types.Grantee{ID: aws.String("u2")}, Permission: types.PermissionRead}}, "custom"},
	} {
		if got, _ := cannedOf("t", c.grants); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestSettle(t *testing.T) {
	c := newFakeAPI(t).client(t, fastPoll)
	// Visible on some nodes first: one disagreeing read starts the count again.
	answers := []bool{true, true, false, true, true, true, true, true, true}
	n := 0
	err := c.settle(t.Context(), func(context.Context) (bool, error) {
		ok := answers[min(n, len(answers)-1)]
		n++
		if !ok {
			return false, errors.New("not on this node yet")
		}
		return true, nil
	})
	if err != nil || n != 9 {
		t.Errorf("err %v after %d reads, want nil after 9", err, n)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	err = c.settle(ctx, func(context.Context) (bool, error) { return false, errors.New("never visible") })
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "never visible") {
		t.Errorf("err %v, want the deadline with the last read's error", err)
	}
}

func TestPutThenSettle_OneWriteAtATimePerBucket(t *testing.T) {
	c := newFakeAPI(t).client(t, fastPoll)
	var mu sync.Mutex
	inFlight, most := 0, 0
	write := func(bucket string) error {
		return putThenSettle(t.Context(), c, bucket, "setting",
			func(context.Context) error {
				mu.Lock()
				inFlight++
				most = max(most, inFlight)
				mu.Unlock()
				return nil
			},
			func(context.Context) (bool, error) { return true, nil }, true)
	}
	done := make(chan error)
	for range 4 {
		go func() {
			err := write("same-bucket")
			mu.Lock()
			inFlight--
			mu.Unlock()
			done <- err
		}()
	}
	for range 4 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if most != 1 {
		t.Errorf("%d writes to one bucket overlapped, want 1 at a time", most)
	}
}

func TestPutThenSettle_LostWriteIsWrittenAgain(t *testing.T) {
	c := newFakeAPI(t).client(t, fastPoll, func(c *Config) { c.MOSLostAfter = 20 * time.Millisecond })
	puts, held := 0, ""
	err := putThenSettle(t.Context(), c, "b", "CORS rules",
		func(context.Context) error {
			puts++
			if puts > 1 { // the first write was overwritten by another writer
				held = "mine"
			}
			return nil
		},
		func(context.Context) (string, error) { return held, nil }, "mine")
	if err != nil || puts != 2 {
		t.Errorf("err %v after %d writes, want nil after 2", err, puts)
	}

	puts = 0
	err = putThenSettle(t.Context(), c, "b", "CORS rules",
		func(context.Context) error { puts++; return nil },
		func(context.Context) (string, error) { return "theirs", nil }, "mine")
	if err == nil || puts != mosSettingTries {
		t.Errorf("err %v after %d writes, want an error after %d", err, puts, mosSettingTries)
	}
}

func TestMOSLock_HonoursContext(t *testing.T) {
	c := newFakeAPI(t).client(t)
	unlock, err := c.mosLock(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.mosLock(ctx, "b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err %v, want the deadline while another write holds the bucket", err)
	}
	if u, err := c.mosLock(t.Context(), "other"); err != nil {
		t.Errorf("another bucket waited: %v", err)
	} else {
		u()
	}
}

package provider

import (
	"crypto/md5" //nolint:gosec // the fake computes ETags as MOS does
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// mosFake is MOS (path-style S3, in memory) and the console's bucket records and archives.
type mosFake struct {
	mu       sync.Mutex
	s3       *httptest.Server
	buckets  map[string]map[string]string // bucket -> key -> body
	records  map[int64]map[string]any     // console storage bucket records
	archives map[string]int64             // archive name -> storage bucket record ID
	archMeta map[string]map[string]any    // archive name -> its other fields (archive_test.go)
	afiles   map[int64]*fakeArchiveFile   // archive file ID -> the file (archive_file_test.go)
	alinks   map[int64][]*fakeArchiveLink // archive file ID -> its links
	acls     map[string]string            // "<bucket>/<key>" (key empty for the bucket) -> canned ACL
	settings map[string]string            // "<bucket>?cors" and so on -> the XML last put
	meta     map[string]map[string]string // "<bucket>/<key>" -> user metadata
	locked   map[string]bool              // buckets created with object lock
	retained map[string][2]string         // "<bucket>/<key>" -> retention mode and until (RFC 3339)
	next     int64
	puts     int // records created or updated, by method
}

func newMOSFake(t *testing.T, f *fakeAPI) *mosFake {
	t.Helper()
	m := &mosFake{buckets: map[string]map[string]string{}, records: map[int64]map[string]any{}, archives: map[string]int64{}, archMeta: map[string]map[string]any{},
		afiles: map[int64]*fakeArchiveFile{}, alinks: map[int64][]*fakeArchiveLink{},
		acls: map[string]string{}, settings: map[string]string{}, meta: map[string]map[string]string{},
		locked: map[string]bool{}, retained: map[string][2]string{}, next: 700}
	m.s3 = httptest.NewTLSServer(http.HandlerFunc(m.serveS3))
	t.Cleanup(m.s3.Close)
	f.mos = http.HandlerFunc(m.serveConsole)
	t.Setenv("MTNCLOUD_MOS_ACCESS_KEY", "test-access")
	t.Setenv("MTNCLOUD_MOS_SECRET_KEY", "test-secret")
	t.Setenv("MTNCLOUD_MOS_ENDPOINT", m.s3.URL)
	return m
}

// config is the provider block for these tests: the fake MOS has a self-signed certificate.
func (m *mosFake) config(f *fakeAPI) string {
	return `
provider "mtncloud" {
  url          = "` + f.srv.URL + `"
  access_token = "test-token"
  secure       = false
}
`
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

func (m *mosFake) serveS3(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	bucket, key := parts[0], ""
	if len(parts) == 2 {
		key = parts[1]
	}
	objs, exists := m.buckets[bucket]
	q := r.URL.Query()
	body, _ := io.ReadAll(r.Body)
	sub := ""
	for _, k := range []string{"acl", "cors", "lifecycle", "logging", "object-lock", "retention", "uploads", "delete"} {
		if q.Has(k) {
			sub = k
		}
	}
	at := bucket + "/" + key
	switch {
	case bucket == "":
		names := slices.Sorted(maps.Keys(m.buckets))
		var b strings.Builder
		b.WriteString(`<ListAllMyBucketsResult><Owner><ID>tenant</ID></Owner><Buckets>`)
		for _, n := range names {
			fmt.Fprintf(&b, `<Bucket><Name>%s</Name><CreationDate>2026-10-29T10:00:00.000Z</CreationDate></Bucket>`, n)
		}
		b.WriteString(`</Buckets></ListAllMyBucketsResult>`)
		_, _ = w.Write([]byte(b.String()))
	case key == "" && sub == "" && r.Method == http.MethodPut:
		if exists {
			s3Error(w, http.StatusConflict, "BucketAlreadyOwnedByYou")
			return
		}
		m.buckets[bucket] = map[string]string{}
		m.locked[bucket] = r.Header.Get("x-amz-bucket-object-lock-enabled") == "true"
	case !exists:
		s3Error(w, http.StatusNotFound, "NoSuchBucket")
	case sub == "acl" && r.Method == http.MethodPut:
		if key != "" && objs[key] == "" {
			s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		m.acls[at] = r.Header.Get("x-amz-acl")
	case sub == "acl":
		if key != "" && objs[key] == "" {
			s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		grants := `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>tenant</ID></Grantee><Permission>FULL_CONTROL</Permission></Grant>`
		switch m.acls[at] {
		case "public-read":
			grants += `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee><Permission>READ</Permission></Grant>`
		case "authenticated-read":
			grants += `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AuthenticatedUsers</URI></Grantee><Permission>READ</Permission></Grant>`
		}
		_, _ = fmt.Fprintf(w, `<AccessControlPolicy><Owner><ID>tenant</ID></Owner><AccessControlList>%s</AccessControlList></AccessControlPolicy>`, grants)
	case key == "" && (sub == "cors" || sub == "lifecycle" || sub == "logging"):
		switch r.Method {
		case http.MethodPut:
			m.settings[bucket+"?"+sub] = string(body)
		case http.MethodDelete:
			delete(m.settings, bucket+"?"+sub)
			w.WriteHeader(http.StatusNoContent)
		default:
			got, ok := m.settings[bucket+"?"+sub]
			switch {
			case ok:
				_, _ = w.Write([]byte(got))
			case sub == "cors":
				s3Error(w, http.StatusNotFound, "NoSuchCORSConfiguration")
			case sub == "lifecycle":
				s3Error(w, http.StatusNotFound, "NoSuchLifecycleConfiguration")
			default:
				_, _ = w.Write([]byte(`<BucketLoggingStatus></BucketLoggingStatus>`))
			}
		}
	case key == "" && sub == "" && r.Method == http.MethodHead:
	case key == "" && sub == "" && r.Method == http.MethodDelete:
		if len(objs) > 0 {
			s3Error(w, http.StatusConflict, "BucketNotEmpty")
			return
		}
		delete(m.buckets, bucket)
		w.WriteHeader(http.StatusNoContent)
	case sub == "object-lock" && !m.locked[bucket]:
		s3Error(w, http.StatusNotFound, "ObjectLockConfigurationNotFoundError")
	case sub == "object-lock" && r.Method == http.MethodPut:
		m.settings[bucket+"?object-lock"] = string(body)
	case sub == "object-lock":
		got, ok := m.settings[bucket+"?object-lock"]
		if !ok {
			got = `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`
		}
		_, _ = w.Write([]byte(got))
	case sub == "retention":
		mode := regexp.MustCompile(`<Mode>([^<]*)</Mode>`).FindStringSubmatch(string(body))
		until := regexp.MustCompile(`<RetainUntilDate>([^<]*)</RetainUntilDate>`).FindStringSubmatch(string(body))
		if mode == nil || until == nil {
			s3Error(w, http.StatusBadRequest, "MalformedXML")
			return
		}
		if old, ok := m.retained[at]; ok && until[1] < old[1] && r.Header.Get("x-amz-bypass-governance-retention") != "true" {
			s3Error(w, http.StatusForbidden, "AccessDenied")
			return
		}
		m.retained[at] = [2]string{mode[1], until[1]}
	case sub == "uploads":
		_, _ = fmt.Fprintf(w, `<ListMultipartUploadsResult><Bucket>%s</Bucket><IsTruncated>false</IsTruncated></ListMultipartUploadsResult>`, bucket)
	case sub == "delete":
		for _, k := range regexp.MustCompile(`<Key>([^<]*)</Key>`).FindAllStringSubmatch(string(body), -1) {
			delete(objs, k[1])
		}
		_, _ = w.Write([]byte(`<DeleteResult></DeleteResult>`))
	case key == "" && r.Method == http.MethodGet:
		prefix := q.Get("prefix")
		keys := make([]string, 0, len(objs))
		for k := range objs {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		var b strings.Builder
		fmt.Fprintf(&b, `<ListBucketResult><Name>%s</Name><IsTruncated>false</IsTruncated><KeyCount>%d</KeyCount>`, bucket, len(keys))
		for _, k := range keys {
			fmt.Fprintf(&b, `<Contents><Key>%s</Key><ETag>"%s"</ETag><Size>%d</Size><LastModified>2026-10-29T10:00:00.000Z</LastModified></Contents>`, k, md5Of(objs[k]), len(objs[k]))
		}
		b.WriteString(`</ListBucketResult>`)
		_, _ = w.Write([]byte(b.String()))
	case r.Method == http.MethodPut:
		if _, held := m.retained[at]; held && objs[key] != "" {
			s3Error(w, http.StatusForbidden, "AccessDenied") // MOS keeps no versions: no overwrite under retention
			return
		}
		objs[key] = string(body)
		meta := map[string]string{}
		for h, v := range r.Header {
			if n, ok := strings.CutPrefix(strings.ToLower(h), "x-amz-meta-"); ok {
				meta[n] = v[0]
			}
		}
		m.meta[at] = meta
		m.acls[at] = r.Header.Get("x-amz-acl") // a new upload is private unless it says otherwise
		if mode := r.Header.Get("x-amz-object-lock-mode"); mode != "" {
			m.retained[at] = [2]string{mode, r.Header.Get("x-amz-object-lock-retain-until-date")}
		}
		w.Header().Set("ETag", `"`+md5Of(objs[key])+`"`)
	case objs[key] == "":
		s3Error(w, http.StatusNotFound, "NoSuchKey")
	case r.Method == http.MethodDelete:
		if _, held := m.retained[at]; held && r.Header.Get("x-amz-bypass-governance-retention") != "true" {
			s3Error(w, http.StatusForbidden, "AccessDenied")
			return
		}
		delete(objs, key)
		delete(m.retained, at)
		w.WriteHeader(http.StatusNoContent)
	default: // HEAD or GET
		w.Header().Set("ETag", `"`+md5Of(objs[key])+`"`)
		w.Header().Set("Last-Modified", "Thu, 29 Oct 2026 10:00:00 GMT")
		w.Header().Set("Content-Length", strconv.Itoa(len(objs[key])))
		if ret, ok := m.retained[at]; ok {
			w.Header().Set("x-amz-object-lock-mode", ret[0])
			w.Header().Set("x-amz-object-lock-retain-until-date", ret[1])
		}
		for n, v := range m.meta[at] {
			w.Header().Set("x-amz-meta-"+n, v)
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(objs[key]))
		}
	}
}

func md5Of(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // the ETag MOS computes
	return hex.EncodeToString(sum[:])
}

func (m *mosFake) serveConsole(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/api/archives") {
		m.serveArchives(w, r)
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/storage-buckets/"), 10, 64)
	switch {
	case r.URL.Path == "/api/storage-buckets" && r.Method == http.MethodGet:
		rows := []any{}
		for _, rec := range m.records {
			if p := r.URL.Query().Get("phrase"); p == "" || strings.Contains(rec["bucketName"].(string), p) {
				rows = append(rows, rec)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"storageBuckets": rows, "meta": map[string]any{"total": len(rows)}})
	case r.URL.Path == "/api/storage-buckets" && r.Method == http.MethodPost:
		var body struct {
			StorageBucket map[string]any `json:"storageBucket"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sb := body.StorageBucket
		if sb["createBucket"] != false {
			w.WriteHeader(http.StatusBadRequest) // the provider must never ask the console to create it
			return
		}
		if _, ok := m.buckets[sb["bucketName"].(string)]; !ok {
			_, _ = w.Write([]byte(`{"success":false,"msg":"Error Validating Storage Provider: NoSuchBucket"}`))
			return
		}
		m.next++
		m.puts++
		rec := map[string]any{"id": m.next, "name": sb["name"], "bucketName": sb["bucketName"], "providerType": "s3", "active": true,
			"config": map[string]any{"endpoint": sb["config"].(map[string]any)["endpoint"], "pathStyleAccess": "on"}}
		m.records[m.next] = rec
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "storageBucket": rec})
	case m.records[id] == nil:
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"storageBucket": m.records[id]})
	case r.Method == http.MethodPut:
		var body struct {
			StorageBucket map[string]any `json:"storageBucket"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.puts++
		m.records[id]["name"] = body.StorageBucket["name"]
		m.records[id]["config"].(map[string]any)["endpoint"] = body.StorageBucket["config"].(map[string]any)["endpoint"]
		_, _ = w.Write([]byte(`{"success":true}`))
	case r.Method == http.MethodDelete:
		for name, a := range m.archives {
			if a == id {
				delete(m.archives, name) // what the real console does, without a word
			}
		}
		delete(m.records, id)
		_, _ = w.Write([]byte(`{"success":true}`))
	}
}

func (m *mosFake) addObject(bucket, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buckets[bucket][key] = "x"
}

func (m *mosFake) checkDestroyed(bucket string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.buckets[bucket]; ok {
			return fmt.Errorf("bucket %s still in MOS", bucket)
		}
		for _, rec := range m.records {
			if rec["bucketName"] == bucket {
				return fmt.Errorf("bucket %s still has console record %v", bucket, rec["id"])
			}
		}
		return nil
	}
}

func TestMOSBucket(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	cfg := func(console string, force bool) string {
		c := m.config(f) + `
resource "mtncloud_mos_bucket" "b" {
  name          = "tf-acc-mos-a1"
  force_destroy = ` + strconv.FormatBool(force) + "\n"
		if console != "" {
			c += `  console_name  = "` + console + `"` + "\n"
		}
		return c + "}\n"
	}
	str := knownvalue.StringExact
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy:             m.checkDestroyed("tf-acc-mos-a1"),
		Steps: []resource.TestStep{
			{
				Config: cfg("", false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_mos_bucket.b", tfjsonpath.New("id"), str("701")),
					statecheck.ExpectKnownValue("mtncloud_mos_bucket.b", tfjsonpath.New("console_name"), str("tf-acc-mos-a1")),
					statecheck.ExpectKnownValue("mtncloud_mos_bucket.b", tfjsonpath.New("endpoint"), str("public")),
					statecheck.ExpectKnownValue("mtncloud_mos_bucket.b", tfjsonpath.New("endpoint_url"), str(m.s3.URL)),
					statecheck.ExpectKnownValue("mtncloud_mos_bucket.b", tfjsonpath.New("object_lock_enabled"), knownvalue.Bool(false)),
				},
			},
			{Config: cfg("", false), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// Renaming the console record is in place.
				Config: cfg("Team data", false),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_mos_bucket.b", plancheck.ResourceActionUpdate)}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_mos_bucket.b", tfjsonpath.New("console_name"), str("Team data")),
				},
			},
			{Config: cfg("Team data", false), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// By name, as well as by ID.
				Config: cfg("Team data", false), ResourceName: "mtncloud_mos_bucket.b", ImportState: true, ImportStateId: "tf-acc-mos-a1",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"force_destroy", "timeouts"},
			},
			{
				// An archive on the bucket: destroy refuses rather than delete it with the record.
				PreConfig: func() {
					m.mu.Lock()
					m.archives["nightly"] = 701
					m.mu.Unlock()
				},
				Config: cfg("Team data", false), Destroy: true,
				ExpectError: regexp.MustCompile(`(?s)Archive "nightly" stores its files in bucket`),
			},
			{
				// Objects in it and no force_destroy: refused, and nothing is lost.
				PreConfig: func() {
					m.mu.Lock()
					delete(m.archives, "nightly")
					m.mu.Unlock()
					m.addObject("tf-acc-mos-a1", "a.txt")
				},
				Config: cfg("Team data", false), Destroy: true,
				ExpectError: regexp.MustCompile(`(?s)isn't empty.*force_destroy`),
			},
			// force_destroy empties it, then the plain destroy at the end removes both sides (CheckDestroy).
			{Config: cfg("Team data", true)},
		},
	})
}

func TestMOSBucket_DeletedOutsideTerraform(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	cfg := m.config(f) + `
resource "mtncloud_mos_bucket" "b" {
  name = "tf-acc-mos-b2"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				// The bucket goes in MOS but its record stays: the plan recreates it, reusing the record.
				PreConfig: func() {
					m.mu.Lock()
					delete(m.buckets, "tf-acc-mos-b2")
					m.mu.Unlock()
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_mos_bucket.b", plancheck.ResourceActionCreate)}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_mos_bucket.b", tfjsonpath.New("id"), knownvalue.StringExact("701")),
				},
			},
		},
	})
}

func TestMOSBucket_ExistingIsNotAdopted(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	m.buckets["taken-bucket"] = map[string]string{"data": "x"}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: m.config(f) + `
resource "mtncloud_mos_bucket" "b" {
  name = "taken-bucket"
}
`,
			ExpectError: regexp.MustCompile(`(?s)already has a bucket named "taken-bucket".*terraform import`),
		}},
	})
	if len(m.buckets["taken-bucket"]) != 1 {
		t.Error("the existing bucket was changed")
	}
}

func TestMOSBucket_PlanTimeChecks(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	for name, c := range map[string]struct{ attrs, want string }{
		"upper case":       {`name = "Bad_Name"`, `isn't a valid bucket name`},
		"like an IP":       {`name = "10.20.0.1"`, `look like an IP address`},
		"unknown endpoint": {"name = \"ok-name\"\n  endpoint = \"internal\"", `value must be one of`},
		"no private one":   {"name = \"ok-name\"\n  endpoint = \"private\"", `no private endpoint`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{{
					Config:      m.config(f) + "resource \"mtncloud_mos_bucket\" \"b\" {\n  " + c.attrs + "\n}\n",
					ExpectError: regexp.MustCompile(c.want),
				}},
			})
		})
	}
	if _, ok := m.buckets["ok-name"]; ok {
		t.Error("a bucket was left in MOS after a refused create")
	}
}

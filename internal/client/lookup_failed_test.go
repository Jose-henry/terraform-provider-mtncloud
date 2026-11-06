package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLenientFlag(t *testing.T) {
	for raw, want := range map[string]bool{
		`true`: true, `false`: false, `"true"`: true, `"false"`: false, `"on"`: true, `"off"`: false, `"ON"`: true,
		`"yes"`: true, `"1"`: true, `""`: false, `"odd"`: false, `null`: false, `1`: true, `0`: false, `{}`: false, `[1]`: false,
	} {
		var got struct {
			F lenientFlag `json:"f"`
		}
		if err := json.Unmarshal([]byte(`{"f":`+raw+`}`), &got); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
		if bool(got.F) != want {
			t.Errorf("%s: got %v, want %v", raw, got.F, want)
		}
	}
}

// The live list that failed: one instance's noAgent is the form's checkbox value (2026-11-06).
func TestListInstances_CheckboxNoAgent(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/instances", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"instances":[
			{"id":1,"name":"a","config":{"noAgent":true}},
			{"id":2,"name":"b","config":{"noAgent":"on"}},
			{"id":3,"name":"c","config":{"noAgent":"off"}},
			{"id":4,"name":"d","config":{}}
		],"meta":{"total":4}}`))
	})
	got, err := f.client(t).ListInstances(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, true, false, false}
	for i, in := range got {
		if in.NoAgent != want[i] {
			t.Errorf("%s: NoAgent %v, want %v", in.Name, in.NoAgent, want[i])
		}
	}
}

func TestLookupFailed(t *testing.T) {
	broken := newFakeAPI(t)
	broken.route("GET", "/api/options/groups", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"msg":"nope"}`))
	})
	broken.route("GET", "/api/instances", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"instances":[{"id":"x"}]}`))
	})
	_, apiErr := broken.client(t).Groups(t.Context())
	_, listErr := broken.client(t).ListInstances(t.Context())
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	login, err := New(Config{URL: gone.URL, Username: "u", Password: "p", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, loginErr := login.Groups(t.Context())

	const failed = "Couldn't check the project"
	for name, tc := range map[string]struct {
		err             error
		failed, unread  bool
		title, inDetail string
	}{
		"api error":     {err: apiErr, failed: true, title: failed, inDetail: "wasn't checked"},
		"undecodable":   {err: listErr, failed: true, unread: true, title: failed, inDetail: "Try again."},
		"login":         {err: loginErr, failed: true, title: failed, inDetail: "the login to MTN Cloud failed"},
		"wrapped":       {err: fmt.Errorf("x: %w", apiErr), failed: true, title: failed, inDetail: "wasn't checked"},
		"canceled":      {err: context.Canceled, failed: true, title: failed, inDetail: "wasn't checked"},
		"not in result": {err: &NoMatchError{msg: "no project"}, title: "Unknown project", inDetail: "no project"},
		"plain":         {err: errors.New("no plan named x"), title: "Unknown project", inDetail: "no plan named x"},
		"nil":           {title: "Unknown project"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := LookupFailed(tc.err); got != tc.failed {
				t.Errorf("LookupFailed: %v, want %v (%v)", got, tc.failed, tc.err)
			}
			if got := IsUnreadable(tc.err); got != tc.unread {
				t.Errorf("IsUnreadable: %v, want %v", got, tc.unread)
			}
			if got := LookupTitle(tc.err, "project", "Unknown project"); got != tc.title {
				t.Errorf("title %q, want %q", got, tc.title)
			}
			if tc.err == nil {
				return
			}
			if got := LookupDetail(tc.err, "project"); !strings.Contains(got, tc.inDetail) {
				t.Errorf("detail %q has no %q", got, tc.inDetail)
			}
			if got := ProjectErrorTitle(tc.err); got != tc.title {
				t.Errorf("ProjectErrorTitle %q, want %q", got, tc.title)
			}
		})
	}
}

func TestDefaultsFor_LookupFailed(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/options/groups", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := f.client(t).DefaultsFor(t.Context()); !LookupFailed(err) {
		t.Errorf("got %v, want the failed lookup", err)
	}
}

package client

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func sgFixture(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "security_groups", file))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// captureBody records the JSON body of the request and answers with a fixture.
func captureBody(t *testing.T, into *map[string]any, status int, reply []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, into); err != nil {
			t.Errorf("request body is not JSON: %s", b)
		}
		w.WriteHeader(status)
		_, _ = w.Write(reply)
	}
}

func TestCreateSecurityGroup_SendsProjectAndCloud(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	var body map[string]any
	f.route("POST", "/api/security-groups", captureBody(t, &body, http.StatusOK, sgFixture(t, "create_response.json")))

	g, err := f.client(t).CreateSecurityGroup(t.Context(), "web", "web servers", 9201)
	if err != nil {
		t.Fatal(err)
	}
	sg := body["securityGroup"].(map[string]any)
	opts := sg["customOptions"].(map[string]any)
	if sg["name"] != "web" || sg["zoneId"] != float64(4) || opts["resourcePoolId"] != float64(9201) {
		t.Errorf("request body = %v", body)
	}
	if g.ID != 9501 || len(g.Locations) != 1 || g.Locations[0].ProjectName != "web-prod-Acme" || g.Locations[0].ProjectID != 9201 || len(g.Rules) != 0 {
		t.Errorf("group = %+v", g)
	}
}

func TestGetSecurityGroup_ParsesEveryRuleShape(t *testing.T) {
	f := newFakeAPI(t)
	body := sgFixture(t, "get_after_sync.json")
	f.route("GET", "/api/security-groups/9501", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })

	g, err := f.client(t).GetSecurityGroup(t.Context(), 9501)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Rules) != 5 {
		t.Fatalf("got %d rules", len(g.Rules))
	}
	byID := map[int64]SecurityGroupRule{}
	for _, r := range g.Rules {
		byID[r.ID] = r
	}
	if r := byID[9701]; deref(r.PortRange) != "22" || deref(r.Source) != "10.0.0.0/24" || deref(r.Name) != "tf-acc-ssh" || r.RuleType != "customRule" {
		t.Errorf("ssh rule = %+v", r)
	}
	if r := byID[9703]; r.Name != nil || r.PortRange != nil || deref(r.Destination) != "0.0.0.0/0" || r.SourceType != "all" || r.RuleType != "custom" {
		t.Errorf("default egress rule = %+v", r)
	}
	if r := byID[9705]; r.SourceGroupID == nil || *r.SourceGroupID != 9502 {
		t.Errorf("group rule = %+v", r)
	}
}

func TestGetSecurityGroup_NotFound(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/security-groups/1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(sgFixture(t, "not_found.json"))
	})
	if _, err := f.client(t).GetSecurityGroup(t.Context(), 1); !IsNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestCreateSecurityGroupRule_Bodies(t *testing.T) {
	for name, tc := range map[string]struct {
		in   SecurityGroupRuleInput
		want map[string]any
	}{
		"cidr": {
			SecurityGroupRuleInput{Name: "ssh", Direction: "ingress", Protocol: "tcp", PortRange: "22", RemoteCIDR: "10.0.0.0/24"},
			map[string]any{"ruleType": "customRule", "destinationType": "instance", "direction": "ingress", "protocol": "tcp",
				"portRange": "22", "sourceType": "cidr", "source": "10.0.0.0/24", "name": "ssh"},
		},
		"group": {
			SecurityGroupRuleInput{Direction: "ingress", Protocol: "any", RemoteGroupID: 9502},
			map[string]any{"ruleType": "customRule", "destinationType": "instance", "direction": "ingress", "protocol": "any",
				"sourceType": "group", "sourceGroup": map[string]any{"id": float64(9502)}},
		},
		"any address, all ports": {
			SecurityGroupRuleInput{Direction: "egress", Protocol: "udp"},
			map[string]any{"ruleType": "customRule", "destinationType": "instance", "direction": "egress", "protocol": "udp", "sourceType": "all"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAPI(t)
			var body map[string]any
			f.route("POST", "/api/security-groups/9501/rules", captureBody(t, &body, http.StatusOK, sgFixture(t, "rule_create_response.json")))
			if _, err := f.client(t).CreateSecurityGroupRule(t.Context(), 9501, tc.in); err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(body["rule"])
			want, _ := json.Marshal(tc.want)
			if string(got) != string(want) {
				t.Errorf("rule body\n got %s\nwant %s", got, want)
			}
		})
	}
	if _, err := newFakeAPI(t).client(t).CreateSecurityGroupRule(t.Context(), 1, SecurityGroupRuleInput{RemoteCIDR: "10.0.0.0/8", RemoteGroupID: 2}); err == nil {
		t.Error("a CIDR and a group together must be refused")
	}
}

func TestDeletes_TreatNotFoundAsDone(t *testing.T) {
	f := newFakeAPI(t)
	gone := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(sgFixture(t, "not_found.json"))
	}
	f.route("DELETE", "/api/security-groups/9501", gone)
	f.route("DELETE", "/api/security-groups/9501/rules/9701", gone)
	c := f.client(t)
	if err := c.DeleteSecurityGroup(t.Context(), 9501); err != nil {
		t.Errorf("group: %v", err)
	}
	if err := c.DeleteSecurityGroupRule(t.Context(), 9501, 9701); err != nil {
		t.Errorf("rule: %v", err)
	}
	f.route("DELETE", "/api/security-groups/9502", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"msg":"Security group is in use"}`))
	})
	if err := c.DeleteSecurityGroup(t.Context(), 9502); err == nil {
		t.Error("other errors must be reported")
	}
}

func TestUpdateSecurityGroup_Body(t *testing.T) {
	f := newFakeAPI(t)
	var body map[string]any
	f.route("PUT", "/api/security-groups/9501", captureBody(t, &body, http.StatusOK, sgFixture(t, "success.json")))
	if err := f.client(t).UpdateSecurityGroup(t.Context(), 9501, "web2", ""); err != nil {
		t.Fatal(err)
	}
	sg := body["securityGroup"].(map[string]any)
	if sg["name"] != "web2" || sg["description"] != "" {
		t.Errorf("body = %v", body)
	}
}

func TestFindSecurityGroup_MatchesNameAndProject(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/security-groups", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "web" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"securityGroups":[
			{"id":1,"name":"web-old","locations":[{"zonePool":{"id":9201,"name":"web-prod-Acme"}}]},
			{"id":2,"name":"web","locations":[{"zonePool":{"id":9202,"name":"data-Acme"}}]},
			{"id":3,"name":"web","locations":[{"zonePool":{"id":9201,"name":"web-prod-Acme"}}]}],"meta":{"total":3}}`))
	})
	c := f.client(t)
	g, err := c.FindSecurityGroup(t.Context(), "web", 9201)
	if err != nil || g == nil || g.ID != 3 {
		t.Fatalf("got %+v, %v", g, err)
	}
	if g, err := c.FindSecurityGroup(t.Context(), "web", 5); err != nil || g != nil {
		t.Errorf("other project: got %+v, %v", g, err)
	}
}

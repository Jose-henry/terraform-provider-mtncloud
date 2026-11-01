package client

import (
	"net/http"
	"strings"
	"testing"
)

// A failed workflow's reason is its last output lines, never one that mentions a secret.
func TestLastLines(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a\n\nError: kafkaVersion must be 4.3.1\nNothing was provisioned.\n", "a Error: kafkaVersion must be 4.3.1 Nothing was provisioned."},
		{"x\ny\nz\nw", "y z w"},
		{"Error: bad\nADMIN_PASSWORD=hunter2\n", "Error: bad"},
		// MTN's own explanations are kept (SMB, live 2026-11-01), with their log prefix.
		{"x\n[2026-11-01 19:31:35] [CRITICAL] One account serves all of them, so setting a password here would rotate it for every one and break their connections.\n" +
			"[2026-11-01 19:31:35] [CRITICAL] Leave the password blank to add this share, or use Update mode to rotate deliberately. Nothing created.",
			"x [2026-11-01 19:31:35] [CRITICAL] One account serves all of them, so setting a password here would rotate it for every one and break their connections. " +
				"[2026-11-01 19:31:35] [CRITICAL] Leave the password blank to add this share, or use Update mode to rotate deliberately. Nothing created."},
		// ...but not when a value rides along with the same words.
		{"ok\n[CRITICAL] Leave the password blank: Hunter2Pw", "ok"},
		{"ok\nLeave the password blank, it was 'Hunterpw'", "ok"},
		{"ok\n[CRITICAL] Leave the password blank. New password is Hunterpw", "ok"},
		{"ok\nLeave the password blank to add this share, or use Update mode to rotate deliberately. Nothing created. pw Hunterpw", "ok"},
		// Every other line that mentions a secret is dropped (review, 2026-11-01).
		{"ok\nGenerated password for user tfuser is Xk9fLm2Q\n+ sshpass -p Hunter2Pw ssh root@10.0.0.5", "ok"},
		{"ok\n+ mysql -u root --password Hunter2Pw -e 'select 1'\n+ echo tfuser:Hunter2Pw | chpasswd", "ok"},
		{"ok\n+ smbpasswd -a tfuser <<< Hunter2Pw\nAdmin password is now Hunter2Pw", "ok"},
		{"ok\nSetting password for tfuser to 'Hunter2Pw'\nError: password 'Hunter2Pw' does not meet the policy", "ok"},
		{"ok\nmysqladmin -uroot password Hunter2Pw\nYour token abc123def456", "ok"},
		{"ok\n{\"password\": \"hunter2\"}\npassword: hunter2\nadmin_pass = hunter2", "ok"},
		{"ok\nAuthorization: Bearer abc.def\nmysql -pS3cret db\nmasked ***", "ok"},
		// A key or certificate block, every line of it, and a value printed under its label.
		{"ok\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo\nshort\n-----END RSA PRIVATE KEY-----", "ok"},
		{"ok\nSecret key\nwJalrXUtnFEMI", "ok"},
		{"ok\nMIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4gP0sQ9xYz", "ok"},
		{"  \n", ""},
	} {
		if got := lastLines(tc.in, 3); got != tc.want {
			t.Errorf("lastLines(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := lastLines(strings.Repeat("x", 900), 3); len(got) != 500 || !strings.HasPrefix(got, "...") {
		t.Errorf("long output: %d characters", len(got))
	}
}

// The reason is the failed task's, past a failed task set with no output, with the order's own
// secret values masked and their lines left out.
func TestExecutionFailure(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/job-executions/32106", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jobExecution":{"process":{"events":[
			{"status":"complete","output":"Validating"},
			{"status":"failed","output":""},
			{"status":"failed","output":"connecting as admin with Kq9-zz81\nError: Invalid msg_engine value 'null'."}]}}}`))
	})
	c := f.client(t)
	got := c.ExecutionFailure(t.Context(), 32106, []string{"Kq9-zz81"})
	if got != "Error: Invalid msg_engine value 'null'." {
		t.Errorf("got %q", got)
	}
	if c.ExecutionFailure(t.Context(), 0, nil) != "" || c.ExecutionFailure(t.Context(), 404, nil) != "" {
		t.Error("no execution should say nothing")
	}
}

func TestWorkflowInstance(t *testing.T) {
	cases := []struct {
		out  string
		id   int64
		name string
	}{
		{"Creating stack\n Instance name : ab-42-MTN-CLOUD-SPH001-1\n Stack : ab-42-MTN-CLOUD-SPH001-1-stack\n" +
			"   Console          : https://console.example/provisioning/instances/4639\n", 4639, "ab-42-MTN-CLOUD-SPH001-1"},
		// Still waiting for the sync: the name only.
		{" Instance name : ab-42-MTN-CLOUD-SPH001-2\nLooking for OpenStack server", 0, "ab-42-MTN-CLOUD-SPH001-2"},
		{"Validating\nDone", 0, ""},
		// A name mentioned mid-line isn't the report.
		{"checked Instance name : x-1 earlier", 0, ""},
	}
	for _, tc := range cases {
		id, name := WorkflowInstance(tc.out)
		if id != tc.id || name != tc.name {
			t.Errorf("WorkflowInstance(%q) = %d, %q; want %d, %q", tc.out, id, name, tc.id, tc.name)
		}
	}
}

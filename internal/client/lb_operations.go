package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Load balancers are reached through one console task, not a REST endpoint (D-031): the provider
// runs the task with the customer's own token, the console runs the load balancer plugin as that
// user, and the plugin's answer is the output of the job's executeTask event. The plugin enforces
// tenancy, so nothing here sends an account, tenant or user.

// lbTaskCode is the code of the public task that runs load balancer operations (owner, 2026-10-29).
const lbTaskCode = "lboperate.terraform"

// ErrLBAccess means the login may not run load balancer operations: the console refused the task.
var ErrLBAccess = errors.New("your role is not allowed to run load balancer operations. Ask your administrator to grant access to the " +
	"load balancer operations task")

// LBError is a refusal or failure the load balancer plugin reported, in its own words.
type LBError struct {
	Action string
	Msg    string
}

func (e *LBError) Error() string { return e.Msg }

// IsLBNotAvailable reports a network the tenant neither owns nor shares. The plugin words it the
// same whether the network exists or not, by design: it is never "gone".
func IsLBNotAvailable(err error) bool {
	var e *LBError
	return errors.As(err, &e) && strings.Contains(e.Msg, "is not available to your tenant")
}

// IsLBNotFound reports a load balancer that no longer exists, by the plugin's own rule (isGone in
// LbOperationsTaskService). "on this network" is not that: the load balancer exists elsewhere, so
// state naming it is wrong, not stale. "Cloud with ID N not found" is not that either.
func IsLBNotFound(err error) bool {
	var e *LBError
	if !errors.As(err, &e) {
		return false
	}
	m := strings.ToLower(e.Msg)
	if strings.Contains(m, "on this network") {
		return false
	}
	return strings.Contains(m, "not found in this project") || strings.Contains(m, "could not be found") || strings.Contains(m, "404") ||
		(strings.Contains(m, "load balancer") && strings.Contains(m, "not found"))
}

// IsLBMissing reports the plugin's answers that mean "no such load balancer here": gone, not on
// this network, no load balancer by that name, or several by that name. Other failures (a stalled
// console, a cloud error) are not this, and must not be reported as "not found".
func IsLBMissing(err error) bool {
	var e *LBError
	if !errors.As(err, &e) {
		return false
	}
	m := strings.ToLower(e.Msg)
	return IsLBNotFound(err) || strings.Contains(m, "was not found on this network") ||
		strings.Contains(m, "no load balancer named") || (strings.Contains(m, "load balancers") && strings.Contains(m, "are named"))
}

// lbEnvelope is the plugin's answer: exactly one object.
type lbEnvelope struct {
	Success       bool            `json:"success"`
	Action        string          `json:"action"`
	Error         string          `json:"error"`
	Retryable     bool            `json:"retryable"`
	NetworkAccess string          `json:"networkAccess"`
	Result        json.RawMessage `json:"result"`
}

type lbJobExecution struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	Process struct {
		Events []struct {
			ProcessType struct {
				Code string `json:"code"`
			} `json:"processType"`
			Output string `json:"output"`
		} `json:"events"`
	} `json:"process"`
}

// lbTaskID finds the operations task once per provider instance, by its code.
func (c *Client) lbTaskID(ctx context.Context) (int64, error) {
	return cached(ctx, &c.cache, "lb-task", func(ctx context.Context) (int64, error) {
		tasks, err := listAll[struct {
			ID   int64  `json:"id"`
			Code string `json:"code"`
		}](ctx, c, "/api/tasks", "tasks", nil)
		if err != nil {
			return 0, err
		}
		for _, t := range tasks {
			if t.Code == lbTaskCode {
				return t.ID, nil
			}
		}
		return 0, ErrLBAccess
	})
}

// lbReads are the actions that change nothing, so running one again is always safe.
func lbRead(action string) bool {
	switch action {
	case "whoami", "list", "read", "fip-options":
		return true
	}
	return false
}

// lbTransient reports a run that did nothing and can be tried again: the console stalled before the
// plugin read its inputs (action is empty then), the job ended with no output (a plugin reload), or
// the plugin said so itself. Writes say so at the top level; reads inside result (lookupFailed,
// retryable), as the guide's error table lists. A run that ran out of time may still be running,
// so it is retried only for a read: repeating a write could make it twice.
func lbTransient(action, status string, env *lbEnvelope) bool {
	if env == nil {
		return status == "error" || (status == lbTimedOut && lbRead(action))
	}
	if env.Success {
		return false
	}
	if env.Action == "" && strings.Contains(env.Error, "did not signal an event") {
		return true
	}
	var inner struct {
		Retryable    bool `json:"retryable"`
		LookupFailed bool `json:"lookupFailed"`
	}
	_ = json.Unmarshal(env.Result, &inner)
	return env.Retryable || inner.Retryable || inner.LookupFailed
}

// lbTimedOut is the status runLBOnce reports when one attempt ran out of time (LBAttempt).
const lbTimedOut = "timed out"

// attemptExpired reports that one attempt ran out of time while the caller's context is still live.
// The rate limiter refuses a wait that would pass the deadline before ctx itself ends, and says so
// with a wrapped DeadlineExceeded, so that counts too.
func attemptExpired(parent, ctx context.Context, err error) bool {
	return parent.Err() == nil && (ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded))
}

// lbMaxMissing bounds how many reads in a row may answer 404 for a job execution that was just
// started: one that never appears (a role that may run the task but not read its jobs) is an
// error, not an endless wait.
const lbMaxMissing = 10

// lbCustomerText keeps the cloud software's name out of plugin messages shown to customers
// (owner's rule, 2026-10-27).
func lbCustomerText(msg string) string {
	for _, p := range []string{"OpenStack ", "Openstack "} { // mtncloud:allow-cloud-name (scrubbing it)
		if strings.HasPrefix(msg, p) {
			msg = "The cloud " + msg[len(p):]
		}
	}
	// Longer names first: NewReplacer prefers the earlier pair where two match at the same place.
	return strings.NewReplacer(
		"the OpenStack", "the cloud", "The OpenStack", "The cloud", // mtncloud:allow-cloud-name (scrubbing it)
		"OpenStack", "the cloud", "Openstack", "the cloud", "openstack", "the cloud", // mtncloud:allow-cloud-name (scrubbing it)
		"Octavia", "the load balancer service", "octavia", "the load balancer service", // mtncloud:allow-cloud-name (scrubbing it)
		"Keystone", "the cloud's sign-in", "keystone", "the cloud's sign-in", // mtncloud:allow-cloud-name (scrubbing it)
		"Barbican", "the certificate store", "barbican", "the certificate store", // mtncloud:allow-cloud-name (scrubbing it)
	).Replace(msg)
}

// lbRetries bounds the retries of a transient run. Each attempt is a new job execution, so it is
// never unbounded.
const lbRetries = 4

// runLB runs one plugin action and returns its result. Transient failures are retried with
// backoff inside ctx; a refusal comes back as *LBError with the plugin's message.
func (c *Client) runLB(ctx context.Context, action string, in map[string]any) (json.RawMessage, error) {
	id, err := c.lbTaskID(ctx)
	if err != nil {
		return nil, err
	}
	opts := map[string]any{"action": action}
	for k, v := range in {
		opts[k] = v
	}
	wait := 10 * c.cfg.PollMin
	for attempt := 0; ; attempt++ {
		status, env, err := c.runLBOnce(ctx, id, action, opts)
		if err != nil {
			return nil, err
		}
		if lbTransient(action, status, env) && attempt < lbRetries {
			c.log(ctx, "load balancer operation did not run, retrying", map[string]any{"action": action, "attempt": attempt + 1})
			if err := sleep(ctx, wait); err != nil {
				return nil, err
			}
			wait *= 2
			continue
		}
		if env == nil {
			return nil, &LBError{Action: action, Msg: fmt.Sprintf("The load balancer %s operation ended (%s) without an answer. Try again.", action, status)}
		}
		if !env.Success {
			msg := env.Error
			if msg == "" {
				msg = fmt.Sprintf("The load balancer %s operation failed without a message.", action)
			}
			return nil, &LBError{Action: action, Msg: lbCustomerText(msg)}
		}
		if len(env.Result) == 0 || string(env.Result) == "null" {
			return nil, &LBError{Action: action, Msg: fmt.Sprintf("The load balancer %s operation answered without a result. Try again.", action)}
		}
		return env.Result, nil
	}
}

// runLBOnce executes the task once and waits for its job to end. env is nil when the job left no
// output.
func (c *Client) runLBOnce(parent context.Context, taskID int64, action string, opts map[string]any) (string, *lbEnvelope, error) {
	ctx, cancel := context.WithTimeout(parent, c.cfg.LBAttempt)
	defer cancel()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	body := map[string]any{"job": map[string]any{
		"name":          "tf-lb-" + action + "-" + hex.EncodeToString(suffix),
		"targetType":    "appliance",
		"customOptions": opts,
	}}
	var started struct {
		JobExecution struct {
			ID int64 `json:"id"`
		} `json:"jobExecution"`
	}
	path := fmt.Sprintf("/api/tasks/%d/execute", taskID)
	if err := c.do(ctx, http.MethodPost, path, nil, body, &started); err != nil {
		if hasStatus(err, http.StatusForbidden) || hasStatus(err, http.StatusNotFound) {
			return "", nil, ErrLBAccess
		}
		if attemptExpired(parent, ctx, err) {
			return lbTimedOut, nil, nil
		}
		return "", nil, err
	}
	if started.JobExecution.ID == 0 {
		return "", nil, fmt.Errorf("MTN Cloud API POST %s: no job execution in the answer", path)
	}
	missing := 0
	je, err := poll(ctx, c, func(ctx context.Context) (lbJobExecution, error) {
		var r struct {
			JobExecution lbJobExecution `json:"jobExecution"`
		}
		err := c.get(ctx, fmt.Sprintf("/api/job-executions/%d", started.JobExecution.ID), nil, &r)
		return r.JobExecution, err
	}, func(je lbJobExecution, err error) (bool, error) {
		if IsNotFound(err) {
			// The execution can take a moment to appear (rule 13), but not forever.
			if missing++; missing >= lbMaxMissing {
				return false, fmt.Errorf("the load balancer job %d could not be read (404 %d times): %w", started.JobExecution.ID, missing, ErrLBAccess)
			}
			return false, nil
		}
		missing = 0
		if err != nil {
			return false, err
		}
		switch je.Status {
		case "", "new", "queued", "pending", "running":
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		if attemptExpired(parent, ctx, err) {
			c.log(parent, "load balancer job ran out of time", map[string]any{"action": action, "job": started.JobExecution.ID})
			return lbTimedOut, nil, nil
		}
		return "", nil, err
	}
	for _, e := range je.Process.Events {
		if e.ProcessType.Code != "executeTask" || strings.TrimSpace(e.Output) == "" {
			continue
		}
		// Output that isn't the envelope (a stack trace, say) counts as no answer.
		var env lbEnvelope
		if json.Unmarshal([]byte(e.Output), &env) == nil {
			return je.Status, &env, nil
		}
	}
	return je.Status, nil, nil
}

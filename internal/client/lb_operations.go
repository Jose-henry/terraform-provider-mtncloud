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
	// RolledBack: the plugin took back what the failed save created (guide, 2026-11-02). LeftBehind
	// counts the pieces it couldn't take back, or had already changed.
	RolledBack bool
	LeftBehind int
	// PieceID is the piece a failed create made anyway: it settled in ERROR (plugin, 2026-11-03).
	PieceID string
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
	// Pending and Piece answer a write, beside result: the save is still running, and the piece
	// as the cloud has it (only once the load balancer has settled).
	Pending bool            `json:"pending"`
	Piece   json.RawMessage `json:"piece"`
	// Warning comes on a save that succeeded but left something in ERROR that the save didn't fail
	// for: an L7 rule on an updated policy (plugin, 2026-11-03).
	Warning string `json:"warning"`
	// AlreadyGone answers a delete of something that isn't there (any more): success, nothing done
	// (plugin 6c47249). Older plugins answer success:false with the same words (isPieceGone).
	AlreadyGone bool `json:"alreadyGone"`
	// A failed save takes back the pools and listeners it created (rolledBack); what it couldn't
	// take back, or had already changed, is in leftBehind (plugin, 2026-11-02).
	RolledBack bool `json:"rolledBack"`
	// Steps of a refused save. An L7 policy taken back after one of its rules failed is marked on
	// its step only (live, t137).
	Steps      []lbStep `json:"steps"`
	LeftBehind []struct {
		Kind  string `json:"kind"`
		Name  string `json:"name"`
		ID    string `json:"id"`
		Error string `json:"error"`
		Kept  bool   `json:"kept"`
	} `json:"leftBehind"`
}

// rolledBack reports a refused save that took back what it created: marked on the answer, or on a
// step (in the answer or its result).
func (e *lbEnvelope) rolledBack() bool {
	if e.RolledBack {
		return true
	}
	steps := e.Steps
	var res struct {
		Steps []lbStep `json:"steps"`
	}
	if len(e.Result) > 0 && json.Unmarshal(e.Result, &res) == nil {
		steps = append(steps, res.Steps...)
	}
	for _, s := range steps {
		if s.RolledBack {
			return true
		}
	}
	return false
}

// failureText is a refused save's message, with what it took back and what it left behind.
func (e *lbEnvelope) failureText(action string) string {
	msg := e.Error
	if msg == "" {
		msg = fmt.Sprintf("The load balancer %s operation failed without a message.", action)
	}
	msg = lbCustomerText(msg)
	if (e.rolledBack() || len(e.LeftBehind) > 0) && !strings.HasSuffix(msg, ".") {
		msg += "." // the cloud's own reasons often end without one (live, t137)
	}
	if e.rolledBack() && len(e.LeftBehind) == 0 {
		msg += " What this save created was removed again, so the next apply starts clean."
	}
	if len(e.LeftBehind) > 0 {
		var left []string
		for _, p := range e.LeftBehind {
			s := fmt.Sprintf("%s %q (%s)", p.Kind, p.Name, p.ID)
			if p.Kept {
				s += ", kept"
			}
			if p.Error != "" {
				s += ": " + lbCustomerText(p.Error)
			}
			left = append(left, s)
		}
		msg += " Left on the load balancer: " + strings.Join(left, "; ") +
			". Check the load balancer before the next apply; import or remove what Terraform doesn't manage."
	}
	return msg
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
// retryable), as the guide's error table lists. A run that ran out of time, or ended with no
// output, may still have acted, so it is retried only when repeating it is safe: a read; a
// create, whose requestId makes the plugin answer with the one already made (existing: true); or a
// delete, which the plugin answers alreadyGone when the first one went through (plugin 6c47249).
func lbTransient(action, status string, env *lbEnvelope) bool {
	if env == nil {
		return (status == "error" || status == lbTimedOut) && (lbRead(action) || lbCreate(action) || lbDelete(action))
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

// lbCreate reports an action that carries a requestId: the load balancer create and the piece
// creates (guide 9.6). The floating IP actions are as safe to repeat: attaching what is attached,
// or detaching what is not, changes nothing (guide 9.6.2).
func lbCreate(action string) bool {
	switch action {
	case "create", "listener-create", "pool-create", "member-add", "monitor-create", "l7policy-create", "fip-attach", "fip-detach":
		return true
	}
	return false
}

// lbDelete reports a delete: the load balancer's and each piece's. Since plugin 6c47249 one of
// something already gone answers success with alreadyGone, so repeating it is safe.
func lbDelete(action string) bool {
	switch action {
	case "delete", "listener-delete", "pool-delete", "member-remove", "monitor-delete", "l7policy-delete":
		return true
	}
	return false
}

// lbTimedOut is the status runLBOnce reports when one attempt ran out of time (LBAttempt for a
// read, LBWriteAttempt for a write).
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
		"Neutron", "the network service", "neutron", "the network service", // mtncloud:allow-cloud-name (scrubbing it)
		"Amphorae", "load balancer VMs", "amphorae", "load balancer VMs", // mtncloud:allow-cloud-name (scrubbing it)
		"Amphora", "Load balancer VM", "amphora", "load balancer VM", // mtncloud:allow-cloud-name (scrubbing it)
	).Replace(msg)
}

// lbRetries bounds the retries of a transient run. Each attempt is a new job execution, so it is
// never unbounded.
const lbRetries = 4

// runLB runs one plugin action and returns its result. Transient failures are retried with
// backoff inside ctx; a refusal comes back as *LBError with the plugin's message.
func (c *Client) runLB(ctx context.Context, action string, in map[string]any) (json.RawMessage, error) {
	env, err := c.runLBEnv(ctx, action, in)
	if err != nil {
		return nil, err
	}
	return env.Result, nil
}

// runLBEnv is runLB with the whole answer, for writes: their pending flag and piece sit beside
// the result.
func (c *Client) runLBEnv(ctx context.Context, action string, in map[string]any) (*lbEnvelope, error) {
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
			e := &LBError{Action: action, Msg: env.failureText(action), RolledBack: env.rolledBack(), LeftBehind: len(env.LeftBehind)}
			// A create whose piece settled in ERROR answers with the piece attached.
			var piece struct {
				ID string `json:"id"`
			}
			if lbCreate(action) && len(env.Piece) > 0 && json.Unmarshal(env.Piece, &piece) == nil {
				e.PieceID = piece.ID
			}
			return nil, e
		}
		if (len(env.Result) == 0 || string(env.Result) == "null") && !env.AlreadyGone {
			return nil, &LBError{Action: action, Msg: fmt.Sprintf("The load balancer %s operation answered without a result. Try again.", action)}
		}
		return env, nil
	}
}

// runLBOnce executes the task once and waits for its job to end. env is nil when the job left no
// output.
func (c *Client) runLBOnce(parent context.Context, taskID int64, action string, opts map[string]any) (string, *lbEnvelope, error) {
	bound := c.cfg.LBWriteAttempt
	if lbRead(action) {
		bound = c.cfg.LBAttempt
	}
	ctx, cancel := context.WithTimeout(parent, bound)
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

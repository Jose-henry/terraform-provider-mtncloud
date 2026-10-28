package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Task is an automation task. Options are its "taskOptions" and Config its plugin options (option
// types with field context "config"), as the API returns them. Body shapes from `tasks add -d` dry
// runs (2026-10-28): {"task": {"name", "taskType": {"code"}, "taskOptions", "config", "credential":
// {"type": "local"} or {"id"}, "executeTarget", "retryable", ...}}.
type Task struct {
	ID int64
	TaskInput
}

// TaskInput is what a create or update sends.
type TaskInput struct {
	Name              string
	Code              string
	TypeCode          string // for example "httpTask"
	Labels            []string
	Visibility        string // "private" or "public"
	ExecuteTarget     string // "local", "resource" or "remote"
	ResultType        string // "", "value", "keyValue" or "json"
	Retryable         bool
	RetryCount        int64
	RetryDelaySeconds int64
	AllowCustomConfig bool
	CredentialID      int64 // 0: the task's own options ("local")
	Options           map[string]any
	Config            map[string]any
}

type wireTask struct {
	ID                int64                 `json:"id"`
	Name              string                `json:"name"`
	Code              *string               `json:"code"`
	TaskType          struct{ Code string } `json:"taskType"`
	Labels            []string              `json:"labels"`
	Visibility        string                `json:"visibility"`
	ExecuteTarget     string                `json:"executeTarget"`
	ResultType        *string               `json:"resultType"`
	Retryable         bool                  `json:"retryable"`
	RetryCount        int64                 `json:"retryCount"`
	RetryDelaySeconds int64                 `json:"retryDelaySeconds"`
	AllowCustomConfig bool                  `json:"allowCustomConfig"`
	Credential        *wireRef              `json:"credential"`
	TaskOptions       map[string]any        `json:"taskOptions"`
	Config            map[string]any        `json:"config"`
}

func (w wireTask) task() *Task {
	t := &Task{ID: w.ID, TaskInput: TaskInput{
		Name: w.Name, TypeCode: w.TaskType.Code, Labels: w.Labels, Visibility: w.Visibility,
		ExecuteTarget: w.ExecuteTarget, Retryable: w.Retryable, RetryCount: w.RetryCount,
		RetryDelaySeconds: w.RetryDelaySeconds, AllowCustomConfig: w.AllowCustomConfig,
		Options: w.TaskOptions, Config: w.Config,
	}}
	if w.Code != nil {
		t.Code = *w.Code
	}
	if w.ResultType != nil {
		t.ResultType = *w.ResultType
	}
	if w.Credential != nil {
		t.CredentialID = w.Credential.ID
	}
	return t
}

func taskBody(in TaskInput) map[string]any {
	t := map[string]any{
		"name":              in.Name,
		"code":              in.Code,
		"taskType":          map[string]any{"code": in.TypeCode},
		"labels":            nonNilStrings(in.Labels),
		"visibility":        in.Visibility,
		"executeTarget":     in.ExecuteTarget,
		"retryable":         in.Retryable,
		"retryCount":        in.RetryCount,
		"retryDelaySeconds": in.RetryDelaySeconds,
		"allowCustomConfig": in.AllowCustomConfig,
		"credential":        map[string]any{"type": "local"},
	}
	if in.ResultType != "" {
		t["resultType"] = in.ResultType
	}
	if in.CredentialID != 0 {
		t["credential"] = map[string]any{"id": in.CredentialID}
	}
	if len(in.Options) > 0 {
		t["taskOptions"] = in.Options
	}
	if len(in.Config) > 0 {
		t["config"] = in.Config
	}
	return map[string]any{"task": t}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// CreateTask creates a task. A retry after an unclear failure must look for it by name first
// (FindTasksNamed): the API makes a second task with the same name.
func (c *Client) CreateTask(ctx context.Context, in TaskInput) (*Task, error) {
	var resp struct {
		Task wireTask `json:"task"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/tasks", nil, taskBody(in), &resp); err != nil {
		return nil, err
	}
	return resp.Task.task(), nil
}

// GetTask reads one task.
func (c *Client) GetTask(ctx context.Context, id int64) (*Task, error) {
	var resp struct {
		Task wireTask `json:"task"`
	}
	if err := c.get(ctx, "/api/tasks/"+itoa(id), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Task.task(), nil
}

// UpdateTask changes a task's settings and returns it. Its type can't change. Every option the
// type has should be in in.Options/in.Config, nil when unset: the platform's own update example
// sends them all, so an option left out may keep its old value.
func (c *Client) UpdateTask(ctx context.Context, id int64, in TaskInput) (*Task, error) {
	var resp struct {
		Task wireTask `json:"task"`
	}
	if err := c.do(ctx, http.MethodPut, "/api/tasks/"+itoa(id), nil, taskBody(in), &resp); err != nil {
		return nil, err
	}
	return resp.Task.task(), nil
}

// DeleteTask deletes a task; one already gone is not an error.
func (c *Client) DeleteTask(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/tasks/"+itoa(id), nil, nil, nil))
}

// FindTasksNamed returns the tasks of one type with exactly this name.
func (c *Client) FindTasksNamed(ctx context.Context, name, typeCode string) ([]*Task, error) {
	items, err := listAll[wireTask](ctx, c, "/api/tasks", "tasks", url.Values{"name": {name}})
	if err != nil {
		return nil, err
	}
	var out []*Task
	for _, w := range items {
		if w.Name == name && w.TaskType.Code == typeCode {
			out = append(out, w.task())
		}
	}
	return out, nil
}

// OptionByName picks one entry of an /api/options/<source> list by name (case ignored), for
// inputs that take an ID but that people name: an AI agent, a Puppet master, a workflow.
func (c *Client) OptionByName(ctx context.Context, source, what, name string) (Option, error) {
	opts, err := c.options(ctx, source, nil)
	if err != nil {
		return Option{}, err
	}
	names := make([]string, len(opts))
	for i, o := range opts {
		if strings.EqualFold(o.Name, name) {
			return o, nil
		}
		names[i] = o.Name
	}
	return Option{}, &NoMatchError{fmt.Sprintf("no %s named %q. Yours: %s", what, name, list(names))}
}

// OptionName is the name of the /api/options/<source> entry with this ID, if there is one.
func (c *Client) OptionName(ctx context.Context, source string, id int64) (string, bool) {
	opts, err := c.options(ctx, source, nil)
	if err != nil {
		return "", false
	}
	for _, o := range opts {
		if o.ID == id {
			return o.Name, true
		}
	}
	return "", false
}

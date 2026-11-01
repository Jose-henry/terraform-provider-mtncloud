package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Workflow is a workflow (task set) with the inputs its form asks for. MTN's own workflows for
// customers are all operational ones (type "operation"), run on existing instances: Create Linux
// User, Install LetsEncrypt SSL and so on (captured 2026-10-27 and 2026-11-01).
type Workflow struct {
	ID     int64
	Name   string
	Type   string // "operation" or "provision"
	Inputs []WorkflowInput
}

// WorkflowInput is one field of a workflow's form.
type WorkflowInput struct {
	FieldName string
	Label     string
	Type      string // "text", "password", "checkbox", "select", ...
	Required  bool
	Default   string
}

// Secret reports whether the input holds a secret (a password field).
func (in WorkflowInput) Secret() bool { return strings.EqualFold(in.Type, "password") }

type wireWorkflow struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	OptionTypes []struct {
		FieldName    string `json:"fieldName"`
		Name         string `json:"name"`
		Type         string `json:"type"`
		Required     bool   `json:"required"`
		DefaultValue any    `json:"defaultValue"`
	} `json:"optionTypes"`
}

func (w wireWorkflow) workflow() Workflow {
	out := Workflow{ID: w.ID, Name: w.Name, Type: w.Type}
	for _, o := range w.OptionTypes {
		def := ""
		if o.DefaultValue != nil {
			def = fmt.Sprint(o.DefaultValue)
		}
		out.Inputs = append(out.Inputs, WorkflowInput{FieldName: o.FieldName, Label: o.Name, Type: o.Type, Required: o.Required, Default: def})
	}
	return out
}

// WorkflowsNamed lists the workflows with this name (any case), with their inputs.
func (c *Client) WorkflowsNamed(ctx context.Context, name string) ([]Workflow, error) {
	var resp struct {
		TaskSets []wireWorkflow `json:"taskSets"`
	}
	if err := c.get(ctx, "/api/task-sets", url.Values{"name": {name}, "max": {"100"}}, &resp); err != nil {
		return nil, err
	}
	var out []Workflow
	for _, w := range resp.TaskSets {
		if strings.EqualFold(w.Name, name) {
			out = append(out, w.workflow())
		}
	}
	return out, nil
}

// RunWorkflow runs a workflow on instances now, as the console's Actions > Run Workflow does
// (POST /api/task-sets/{id}/execute, targetType instance), and returns the job execution's ID.
func (c *Client) RunWorkflow(ctx context.Context, workflowID int64, instanceIDs []int64, inputs map[string]string) (int64, error) {
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	opts := map[string]any{}
	for k, v := range inputs {
		opts[k] = v
	}
	body := map[string]any{"job": map[string]any{
		"name":          "tf-workflow-" + hex.EncodeToString(suffix),
		"targetType":    "instance",
		"instances":     instanceIDs,
		"customOptions": opts,
	}}
	var started struct {
		JobExecution struct {
			ID int64 `json:"id"`
		} `json:"jobExecution"`
	}
	path := fmt.Sprintf("/api/task-sets/%d/execute", workflowID)
	if err := c.do(ctx, http.MethodPost, path, nil, body, &started); err != nil {
		return 0, err
	}
	if started.JobExecution.ID == 0 {
		return 0, fmt.Errorf("MTN Cloud API POST %s: no job execution in the answer", path)
	}
	return started.JobExecution.ID, nil
}

// JobExecution is a run's outcome.
type JobExecution struct {
	ID     int64
	Status string
}

// WaitForJobExecution polls a run until it ends. A 404 straight after the run starts is read as
// not there yet.
func (c *Client) WaitForJobExecution(ctx context.Context, id int64) (*JobExecution, error) {
	return poll(ctx, c, func(ctx context.Context) (*JobExecution, error) {
		var r struct {
			JobExecution struct {
				ID     int64  `json:"id"`
				Status string `json:"status"`
			} `json:"jobExecution"`
		}
		if err := c.get(ctx, "/api/job-executions/"+itoa(id), nil, &r); err != nil {
			return nil, err
		}
		return &JobExecution{ID: r.JobExecution.ID, Status: r.JobExecution.Status}, nil
	}, func(je *JobExecution, err error) (bool, error) {
		switch {
		case IsNotFound(err):
			return false, nil
		case err != nil:
			return false, err
		}
		done, _ := RunDone(je.Status)
		return done, nil
	})
}

// RunDone reports whether a job execution's status has ended, and whether it succeeded.
func RunDone(status string) (done, ok bool) {
	switch strings.ToLower(status) {
	case "complete", "completed", "success", "successful":
		return true, true
	case "error", "failed", "failure", "cancelled", "canceled", "expired": //nolint:misspell // both spellings are API values
		return true, false
	}
	return false, false
}

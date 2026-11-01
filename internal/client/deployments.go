package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Deployments are the console's Provisioning > Deployments: a named deployment, its versions (files
// uploaded to MTN Cloud, a git repository, or a URL to fetch), and deploys of a version to an
// instance. Live QA 2026-11-01 (customer login): a deployment, its versions and their files can be
// created, changed and deleted; deleting a deployment deletes its versions; a version's deployType,
// URLs and version string change with PUT; a file uploaded with ?filename= lands at the version's top
// level, overwriting one of the same name; files in a folder can't be listed or deleted (HTTP 500).

// Deployment is a deployment record.
type Deployment struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DeploymentVersion is one version of a deployment.
type DeploymentVersion struct {
	ID           int64  `json:"id"`
	DeploymentID int64  `json:"deploymentId"`
	Version      string `json:"userVersion"`
	DeployType   string `json:"deployType"` // file, git or fetch
	FetchURL     string `json:"fetchUrl"`
	GitURL       string `json:"gitUrl"`
	GitRef       string `json:"gitRef"`
	Status       string `json:"status"`
}

// DeploymentFile is a file at a version's top level.
type DeploymentFile struct {
	Name      string `json:"name"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"contentLength"`
}

// InstanceDeploy is a version deployed to an instance. Status is "committed" for the one live on the
// instance and "archived" for one another deploy has replaced.
type InstanceDeploy struct {
	ID           int64
	InstanceID   int64
	DeploymentID int64
	VersionID    int64
	Version      string
	Status       string
	Config       map[string]any
	Created      time.Time // zero when the API didn't say
}

func deploymentPath(id int64) string { return "/api/deployments/" + itoa(id) }

func versionPath(deploymentID, id int64) string {
	return deploymentPath(deploymentID) + "/versions/" + itoa(id)
}

// GetDeployment reads one deployment.
func (c *Client) GetDeployment(ctx context.Context, id int64) (*Deployment, error) {
	var resp struct {
		Deployment *Deployment `json:"deployment"`
	}
	if err := c.get(ctx, deploymentPath(id), nil, &resp); err != nil {
		return nil, err
	}
	if resp.Deployment == nil {
		return nil, fmt.Errorf("MTN Cloud API GET %s: no deployment in the answer", deploymentPath(id))
	}
	return resp.Deployment, nil
}

// DeploymentsNamed lists the deployments with this name (any case).
func (c *Client) DeploymentsNamed(ctx context.Context, name string) ([]Deployment, error) {
	var resp struct {
		Deployments []Deployment `json:"deployments"`
	}
	if err := c.get(ctx, "/api/deployments", url.Values{"name": {name}, "max": {"100"}}, &resp); err != nil {
		return nil, err
	}
	var out []Deployment
	for _, d := range resp.Deployments {
		if strings.EqualFold(d.Name, name) {
			out = append(out, d)
		}
	}
	return out, nil
}

// CreateDeployment makes a deployment.
func (c *Client) CreateDeployment(ctx context.Context, name, description string) (*Deployment, error) {
	var resp struct {
		Deployment *Deployment `json:"deployment"`
	}
	body := map[string]any{"deployment": map[string]any{"name": name, "description": description}}
	if err := c.do(ctx, http.MethodPost, "/api/deployments", nil, body, &resp); err != nil {
		return nil, err
	}
	if resp.Deployment == nil || resp.Deployment.ID == 0 {
		return nil, fmt.Errorf("MTN Cloud API POST /api/deployments: no deployment in the answer")
	}
	return resp.Deployment, nil
}

// UpdateDeployment changes a deployment's name and description.
func (c *Client) UpdateDeployment(ctx context.Context, id int64, name, description string) (*Deployment, error) {
	var resp struct {
		Deployment *Deployment `json:"deployment"`
	}
	body := map[string]any{"deployment": map[string]any{"name": name, "description": description}}
	if err := c.do(ctx, http.MethodPut, deploymentPath(id), nil, body, &resp); err != nil {
		return nil, err
	}
	if resp.Deployment == nil {
		return c.GetDeployment(ctx, id)
	}
	return resp.Deployment, nil
}

// DeleteDeployment deletes a deployment and its versions. One already gone is not an error.
func (c *Client) DeleteDeployment(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, deploymentPath(id), nil, nil, nil))
}

// DeploymentVersionInput is what a version is made or changed with.
type DeploymentVersionInput struct {
	Version, DeployType, FetchURL, GitURL, GitRef string
}

func (in DeploymentVersionInput) body() map[string]any {
	v := map[string]any{"version": in.Version, "deployType": in.DeployType}
	// Every URL is sent, empty when unused, so a change of type leaves nothing behind.
	v["fetchUrl"], v["gitUrl"], v["gitRef"] = in.FetchURL, in.GitURL, in.GitRef
	return map[string]any{"version": v}
}

// GetDeploymentVersion reads one version.
func (c *Client) GetDeploymentVersion(ctx context.Context, deploymentID, id int64) (*DeploymentVersion, error) {
	var resp struct {
		Version *DeploymentVersion `json:"version"`
	}
	if err := c.get(ctx, versionPath(deploymentID, id), nil, &resp); err != nil {
		return nil, err
	}
	if resp.Version == nil {
		return nil, fmt.Errorf("MTN Cloud API GET %s: no version in the answer", versionPath(deploymentID, id))
	}
	return resp.Version, nil
}

// DeploymentVersions lists a deployment's versions.
func (c *Client) DeploymentVersions(ctx context.Context, deploymentID int64) ([]DeploymentVersion, error) {
	var resp struct {
		Versions []DeploymentVersion `json:"versions"`
	}
	err := c.get(ctx, deploymentPath(deploymentID)+"/versions", url.Values{"max": {"1000"}}, &resp)
	return resp.Versions, err
}

// CreateDeploymentVersion adds a version to a deployment.
func (c *Client) CreateDeploymentVersion(ctx context.Context, deploymentID int64, in DeploymentVersionInput) (*DeploymentVersion, error) {
	var resp struct {
		Version *DeploymentVersion `json:"version"`
	}
	if err := c.do(ctx, http.MethodPost, deploymentPath(deploymentID)+"/versions", nil, in.body(), &resp); err != nil {
		return nil, err
	}
	if resp.Version == nil || resp.Version.ID == 0 {
		return nil, fmt.Errorf("MTN Cloud API POST %s/versions: no version in the answer", deploymentPath(deploymentID))
	}
	return resp.Version, nil
}

// UpdateDeploymentVersion changes a version's version string and source in place.
func (c *Client) UpdateDeploymentVersion(ctx context.Context, deploymentID, id int64, in DeploymentVersionInput) (*DeploymentVersion, error) {
	var resp struct {
		Version *DeploymentVersion `json:"version"`
	}
	if err := c.do(ctx, http.MethodPut, versionPath(deploymentID, id), nil, in.body(), &resp); err != nil {
		return nil, err
	}
	if resp.Version == nil {
		return c.GetDeploymentVersion(ctx, deploymentID, id)
	}
	return resp.Version, nil
}

// DeleteDeploymentVersion deletes a version and its files. One already gone is not an error. MTN
// Cloud refuses (400 "Unable to delete version because it has been used in deploys.") while an
// instance it was deployed to exists, including one still being deleted in the same destroy (live
// QA 2026-11-01: refused during the VM's delete, deleted once the VM was gone). That refusal is waited
// out for up to Config.VersionInUseWait, then returned with what to do.
func (c *Client) DeleteDeploymentVersion(ctx context.Context, deploymentID, id int64) error {
	start := time.Now()
	_, err := poll(ctx, c, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, ignoreNotFound(c.do(ctx, http.MethodDelete, versionPath(deploymentID, id), nil, nil, nil))
	}, func(_ struct{}, err error) (bool, error) {
		if err == nil {
			return true, nil
		}
		if versionInUse(err) && time.Since(start) < c.cfg.VersionInUseWait {
			return false, nil
		}
		return false, err
	})
	if versionInUse(err) {
		return fmt.Errorf("%w. MTN Cloud deletes a version only once no instance it was deployed to is left: delete those "+
			"instances (or their mtncloud_instance_deploy and the instances), then destroy again", err)
	}
	return err
}

func versionInUse(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusBadRequest && strings.Contains(strings.ToLower(ae.Message), "used in deploys")
}

// DeploymentFiles lists the files at a version's top level.
func (c *Client) DeploymentFiles(ctx context.Context, deploymentID, versionID int64) ([]DeploymentFile, error) {
	var resp struct {
		Files []DeploymentFile `json:"files"`
	}
	err := c.get(ctx, versionPath(deploymentID, versionID)+"/files", nil, &resp)
	return resp.Files, err
}

// UploadDeploymentFile writes a file at a version's top level, overwriting one of the same name.
func (c *Client) UploadDeploymentFile(ctx context.Context, deploymentID, versionID int64, name string, content []byte) error {
	if content == nil {
		content = []byte{}
	}
	return c.doPayload(ctx, http.MethodPost, versionPath(deploymentID, versionID)+"/files", url.Values{"filename": {name}},
		content, "application/octet-stream", nil)
}

// DeleteDeploymentFile deletes a file at a version's top level. One already gone is not an error.
func (c *Client) DeleteDeploymentFile(ctx context.Context, deploymentID, versionID int64, name string) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, versionPath(deploymentID, versionID)+"/files/"+url.PathEscape(name), nil, nil, nil))
}

type wireDeploy struct {
	ID                  int64          `json:"id"`
	InstanceID          int64          `json:"instanceId"`
	DeploymentVersionID int64          `json:"deploymentVersionId"`
	Status              string         `json:"status"`
	Config              map[string]any `json:"config"`
	DateCreated         string         `json:"dateCreated"`
	Deployment          *struct {
		ID int64 `json:"id"`
	} `json:"deployment"`
	DeploymentVersion *struct {
		ID          int64  `json:"id"`
		UserVersion string `json:"userVersion"`
	} `json:"deploymentVersion"`
}

func (w wireDeploy) deploy() InstanceDeploy {
	d := InstanceDeploy{ID: w.ID, InstanceID: w.InstanceID, VersionID: w.DeploymentVersionID, Status: w.Status, Config: w.Config}
	d.Created, _ = time.Parse(time.RFC3339, w.DateCreated)
	if w.Deployment != nil {
		d.DeploymentID = w.Deployment.ID
	}
	if w.DeploymentVersion != nil {
		if d.VersionID == 0 {
			d.VersionID = w.DeploymentVersion.ID
		}
		d.Version = w.DeploymentVersion.UserVersion
	}
	return d
}

// InstanceDeploys lists the deploys on an instance, newest first as the API gives them.
func (c *Client) InstanceDeploys(ctx context.Context, instanceID int64) ([]InstanceDeploy, error) {
	var resp struct {
		Deploys []wireDeploy `json:"appDeploys"`
	}
	if err := c.get(ctx, "/api/instances/"+itoa(instanceID)+"/deploys", url.Values{"max": {"1000"}}, &resp); err != nil {
		return nil, err
	}
	out := make([]InstanceDeploy, 0, len(resp.Deploys))
	for _, w := range resp.Deploys {
		out = append(out, w.deploy())
	}
	return out, nil
}

// DeployToInstance deploys a version to an instance; MTN Cloud runs it straight away. The deploy it
// replaces on the instance becomes archived.
func (c *Client) DeployToInstance(ctx context.Context, instanceID, versionID int64, config map[string]any) (*InstanceDeploy, error) {
	ad := map[string]any{"versionId": versionID}
	if len(config) > 0 {
		ad["config"] = config
	}
	var resp struct {
		Deploy *wireDeploy `json:"appDeploy"`
	}
	path := "/api/instances/" + itoa(instanceID) + "/deploys"
	if err := c.do(ctx, http.MethodPost, path, nil, map[string]any{"appDeploy": ad}, &resp); err != nil {
		return nil, err
	}
	if resp.Deploy == nil || resp.Deploy.ID == 0 {
		return nil, fmt.Errorf("MTN Cloud API POST %s: no deploy in the answer", path)
	}
	d := resp.Deploy.deploy()
	if d.InstanceID == 0 {
		d.InstanceID = instanceID
	}
	return &d, nil
}

// DeployDone reports whether a deploy's status has settled, and whether it failed.
func DeployDone(status string) (done, failed bool) {
	switch strings.ToLower(status) {
	case "", "staged", "pending", "deploying", "running", "queued", "provisioning":
		return false, false
	case "failed", "error", "failure":
		return true, true
	}
	return true, false
}

// WaitForDeploy polls an instance until the deploy settles. A deploy missing from the list straight
// after it was made is read as not there yet.
func (c *Client) WaitForDeploy(ctx context.Context, instanceID, deployID int64) (*InstanceDeploy, error) {
	return poll(ctx, c, func(ctx context.Context) (*InstanceDeploy, error) {
		ds, err := c.InstanceDeploys(ctx, instanceID)
		if err != nil {
			return nil, err
		}
		for i := range ds {
			if ds[i].ID == deployID {
				return &ds[i], nil
			}
		}
		return nil, nil
	}, func(d *InstanceDeploy, err error) (bool, error) {
		if err != nil {
			return false, err
		}
		if d == nil {
			return false, nil
		}
		done, _ := DeployDone(d.Status)
		return done, nil
	})
}

// DeleteDeploy deletes a deploy's record. MTN Cloud deletes only archived ones: the one live on an
// instance stays until another deploy replaces it. One already gone is not an error.
func (c *Client) DeleteDeploy(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/deploys/"+itoa(id), nil, nil, nil))
}

// DeployRun is the instance process that ran a deploy. MTN Cloud marks a deploy committed even when
// its run failed (live QA 2026-11-01: deploy 11 "committed" while process 32833 "failed" with
// "Failed to build deploy config. This Container Node Type may be missing a deploy folder
// configuration."), so the run is what says whether the files reached the instance. Nothing in the
// process names its deploy (refType, refId, subType and subId are null, and it has no events), so a
// deploy's run is found by time: the first deploy process on the instance that started at or after
// the deploy was created (live: 4-5 s after) and before the next deploy there was.
type DeployRun struct {
	ID      int64
	Status  string // running, complete, failed, ...
	Message string
	Start   time.Time
}

type wireProcess struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	StartDate   string `json:"startDate"`
	ProcessType struct {
		Code string `json:"code"`
	} `json:"processType"`
}

func (w wireProcess) run() DeployRun {
	r := DeployRun{ID: w.ID, Status: w.Status, Message: w.Message}
	r.Start, _ = time.Parse(time.RFC3339, w.StartDate)
	return r
}

// LockInstanceDeploys holds deploys to one instance to one at a time, until the deploy's run is
// known: two deploys of this provider then never overlap, and each one's run is the first after it.
func (c *Client) LockInstanceDeploys(ctx context.Context, instanceID int64) (func(), error) {
	return c.keyLock(ctx, "deploy:"+itoa(instanceID), "instance "+itoa(instanceID)+"'s deploys")
}

// GetDeployRun reads one process of the instance history by ID.
func (c *Client) GetDeployRun(ctx context.Context, id int64) (*DeployRun, error) {
	var resp struct {
		Process *wireProcess `json:"process"`
	}
	if err := c.get(ctx, "/api/processes/"+itoa(id), nil, &resp); err != nil {
		return nil, err
	}
	if resp.Process == nil {
		return nil, &APIError{Method: http.MethodGet, Path: "/api/processes/" + itoa(id), StatusCode: http.StatusNotFound, Message: "no process in the answer"}
	}
	r := resp.Process.run()
	return &r, nil
}

// deployRunPages bounds the history read for one deploy (newest first, deployRunPage at a time).
const (
	deployRunPage  = 100
	deployRunPages = 20
)

// DeployRunFor is the deploy's run: the first deploy process on the instance that started at or after
// the deploy was created and, when before isn't zero, before it (the next deploy's creation). The
// history is read newest first, page by page, until it reaches processes older than the deploy. Nil
// when there is no such run yet, or when the deploy's creation time is unknown.
func (c *Client) DeployRunFor(ctx context.Context, d *InstanceDeploy, before time.Time) (*DeployRun, error) {
	if d == nil || d.Created.IsZero() {
		return nil, nil
	}
	var run *DeployRun
	for page := 0; page < deployRunPages; page++ {
		var resp struct {
			Processes []wireProcess `json:"processes"`
		}
		q := url.Values{"instanceId": {itoa(d.InstanceID)}, "max": {itoa(deployRunPage)}, "offset": {itoa(int64(page * deployRunPage))}}
		if err := c.get(ctx, "/api/processes", q, &resp); err != nil {
			return nil, err
		}
		older := false
		for _, p := range resp.Processes {
			r := p.run()
			if r.Start.IsZero() {
				continue
			}
			if r.Start.Before(d.Created) {
				older = true
				continue
			}
			if !strings.EqualFold(p.ProcessType.Code, "deploy") || (!before.IsZero() && !r.Start.Before(before)) {
				continue
			}
			if run == nil || r.Start.Before(run.Start) || (r.Start.Equal(run.Start) && r.ID < run.ID) {
				run = &r
			}
		}
		if older || len(resp.Processes) < deployRunPage {
			break
		}
	}
	return run, nil
}

// Done reports whether a process has finished, and whether it failed.
func (r *DeployRun) Done() (done, failed bool) {
	switch strings.ToLower(r.Status) {
	case "complete", "completed", "success":
		return true, false
	case "failed", "error", "failure", "cancelled", "canceled": //nolint:misspell // the API's words
		return true, true
	}
	return false, false
}

// WaitForDeployRun waits for the deploy's run to finish. Nil, nil when the deploy's creation time is
// unknown (straight away) or no run has appeared within appear: the caller can't tell how it went.
func (c *Client) WaitForDeployRun(ctx context.Context, d *InstanceDeploy, appear time.Duration) (*DeployRun, error) {
	if d == nil || d.Created.IsZero() {
		return nil, nil
	}
	start := time.Now()
	return poll(ctx, c, func(ctx context.Context) (*DeployRun, error) {
		return c.DeployRunFor(ctx, d, time.Time{})
	}, func(r *DeployRun, err error) (bool, error) {
		if err != nil {
			return false, err
		}
		if r == nil {
			return time.Since(start) > appear, nil
		}
		done, _ := r.Done()
		return done, nil
	})
}

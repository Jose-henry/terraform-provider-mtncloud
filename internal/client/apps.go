package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// AppDetail is one app as GET /api/apps/{id} shows it (read live 2026-10-31): its settings, owner
// and tiers. Environment is the environment's code ("dev").
type AppDetail struct {
	ID          int64
	Name        string
	Description string
	Labels      []string
	Environment string
	OwnerID     int64
	OwnerName   string
	GroupName   string
	Status      string
	// Tiers maps each instance in the app to its tier's name, for example 9701 -> "Master".
	Tiers map[int64]string
}

type wireAppDetail struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description *string  `json:"description"`
	Labels      []string `json:"labels"`
	Environment *string  `json:"environment"`
	Status      string   `json:"status"`
	Owner       *struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"owner"`
	Group *struct {
		Name string `json:"name"`
	} `json:"group"`
	AppTiers []struct {
		Tier struct {
			Name string `json:"name"`
		} `json:"tier"`
		AppInstances []struct {
			Instance struct {
				ID int64 `json:"id"`
			} `json:"instance"`
		} `json:"appInstances"`
	} `json:"appTiers"`
}

// GetApp reads one app.
func (c *Client) GetApp(ctx context.Context, id int64) (*AppDetail, error) {
	var resp struct {
		App wireAppDetail `json:"app"`
	}
	if err := c.get(ctx, "/api/apps/"+itoa(id), nil, &resp); err != nil {
		return nil, err
	}
	w := resp.App
	a := &AppDetail{ID: w.ID, Name: w.Name, Description: deref(w.Description), Labels: w.Labels, Environment: deref(w.Environment),
		Status: w.Status, Tiers: map[int64]string{}}
	if w.Owner != nil {
		a.OwnerID, a.OwnerName = w.Owner.ID, w.Owner.Username
	}
	if w.Group != nil {
		a.GroupName = w.Group.Name
	}
	for _, t := range w.AppTiers {
		for _, ai := range t.AppInstances {
			a.Tiers[ai.Instance.ID] = t.Tier.Name
		}
	}
	return a, nil
}

// AppUpdate is what UpdateApp sends; a nil field is left as it is.
type AppUpdate struct {
	Name, Description, Environment *string
	Labels                         []string // sent when SetLabels; empty clears them
	SetLabels                      bool
	OwnerID                        int64 // 0: unchanged. Changing it also changes every instance's owner.
}

// UpdateApp changes an app's settings in place (PUT /api/apps/{id}, body {"app": {...}}; CLI
// `apps update -d`, 2026-10-31).
func (c *Client) UpdateApp(ctx context.Context, id int64, u AppUpdate) error {
	app := map[string]any{}
	if u.Name != nil {
		app["name"] = *u.Name
	}
	if u.Description != nil {
		app["description"] = *u.Description
	}
	if u.Environment != nil {
		app["environment"] = *u.Environment
	}
	if u.SetLabels {
		app["labels"] = append([]string{}, u.Labels...)
	}
	if u.OwnerID != 0 {
		app["ownerId"] = u.OwnerID
	}
	if len(app) == 0 {
		return nil
	}
	return c.do(ctx, http.MethodPut, "/api/apps/"+itoa(id), nil, map[string]any{"app": app}, nil)
}

// AddAppInstance puts an existing instance in an app's tier (POST /api/apps/{id}/add-instance, body
// {"instanceId": N, "tierName": "App"}).
func (c *Client) AddAppInstance(ctx context.Context, appID, instanceID int64, tier string) error {
	body := map[string]any{"instanceId": instanceID, "tierName": tier}
	return c.do(ctx, http.MethodPost, "/api/apps/"+itoa(appID)+"/add-instance", nil, body, nil)
}

// UserIDByName is the ID of the user with this username in the caller's tenant (case ignored).
func (c *Client) UserIDByName(ctx context.Context, username string) (int64, error) {
	recs, err := c.ListRecords(ctx, "/api/users", "users", url.Values{"phrase": {username}})
	if err != nil {
		return 0, fmt.Errorf("listing users: %w", err)
	}
	var names []string
	for _, r := range recs {
		n, _ := r["username"].(string)
		if strings.EqualFold(n, username) {
			id, _ := r["id"].(float64)
			return int64(id), nil
		}
		names = append(names, n)
	}
	return 0, fmt.Errorf("no user named %q in this tenant%s", username, available(names))
}

func available(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " (close matches: " + strings.Join(names, ", ") + ")"
}

// AppCreate is a blank app (no blueprint): instances join it later with AddAppInstance.
type AppCreate struct {
	Name, Description, Environment string // Environment is the code ("dev"); empty: none
	Labels                         []string
	GroupID, CloudID               int64
}

// CreateApp makes a blank app (POST /api/apps with blueprintId "existing", the body `apps add -b
// existing -d` prints, 2026-10-31) and returns its ID.
func (c *Client) CreateApp(ctx context.Context, a AppCreate) (int64, error) {
	body := map[string]any{"blueprintId": "existing", "templateId": "existing", "name": a.Name,
		"group": map[string]any{"id": a.GroupID}, "defaultCloud": map[string]any{"id": a.CloudID}}
	if a.Description != "" {
		body["description"] = a.Description
	}
	if len(a.Labels) > 0 {
		body["labels"] = a.Labels
	}
	if a.Environment != "" {
		body["environment"] = a.Environment
	}
	var resp struct {
		App struct {
			ID int64 `json:"id"`
		} `json:"app"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/apps", nil, body, &resp); err != nil {
		return 0, err
	}
	if resp.App.ID == 0 {
		return 0, fmt.Errorf("MTN Cloud accepted app %q but returned no ID", a.Name)
	}
	return resp.App.ID, nil
}

// DeleteAppOnly deletes an app and leaves its instances running, with their floating IPs (the API
// releases them by default). One already gone counts as deleted.
func (c *Client) DeleteAppOnly(ctx context.Context, id int64) error {
	q := url.Values{"removeInstances": {"off"}, "releaseFloatingIps": {"off"}}
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/apps/"+itoa(id), q, nil, nil))
}

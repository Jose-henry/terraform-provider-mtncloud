package client

import (
	"context"
	"net/url"
)

// Record is one object from a list endpoint, decoded loosely ({"name": ..., "zone": {...}}).
// The read-only lookups that only show what the API returns pick their fields from it, so each
// one is a table entry instead of a typed struct, a wire struct and a converter.
type Record map[string]any

// ListRecords reads every page of a list endpoint, for example ("/api/apps", "apps").
func (c *Client) ListRecords(ctx context.Context, path, key string, q url.Values) ([]Record, error) {
	return listAll[Record](ctx, c, path, key, q)
}

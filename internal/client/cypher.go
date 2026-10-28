package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Cypher is MTN Cloud's secret store. A key starts with its mount: secret/, tfvars/, password/,
// uuid/, key/ or vault/. Reading a missing password/, uuid/ or key/ key CREATES it (API reference,
// "Read or Create a Cypher Key"), so read-only callers check CypherKeyExists first.

// CypherSecret is one key's decrypted value. Data is a string, or a map for an object stored on
// the secret/ mount. Never log it.
type CypherSecret struct {
	Value        string            // the string value, or the object as JSON
	Values       map[string]string // the object's entries; nil for a string value
	LeaseSeconds int64
}

func cypherPath(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/api/cypher/" + strings.Join(segs, "/")
}

// CypherKeyExists reports whether the key is in the store, without reading or creating it.
func (c *Client) CypherKeyExists(ctx context.Context, key string) (bool, Record, error) {
	// "key" narrows the list on the server; the match below stays exact.
	all, err := c.ListRecords(ctx, "/api/cypher", "cyphers", url.Values{"key": {key}})
	if err != nil {
		return false, nil, err
	}
	for _, r := range all {
		if k, _ := r["itemKey"].(string); k == key {
			return true, r, nil
		}
	}
	return false, nil, nil
}

// ReadCypher decrypts one key. For password/, uuid/ and key/ keys, check CypherKeyExists first:
// this call creates them when they are missing.
func (c *Client) ReadCypher(ctx context.Context, key string) (*CypherSecret, error) {
	var resp struct {
		Data          json.RawMessage `json:"data"`
		LeaseDuration int64           `json:"lease_duration"`
	}
	if err := c.get(ctx, cypherPath(key), nil, &resp); err != nil {
		return nil, err
	}
	return decodeCypher(resp.Data, resp.LeaseDuration)
}

func decodeCypher(data json.RawMessage, lease int64) (*CypherSecret, error) {
	out := &CypherSecret{LeaseSeconds: lease}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		out.Value = s
		return out, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("MTN Cloud API: Cypher value is neither a string nor an object") // never quote the value
	}
	out.Values = make(map[string]string, len(obj))
	for k, v := range obj {
		if s, ok := v.(string); ok {
			out.Values[k] = s
		} else {
			b, _ := json.Marshal(v)
			out.Values[k] = string(b)
		}
	}
	out.Value = string(data)
	return out, nil
}

// WriteCypher creates or updates a key. password/, uuid/ and key/ keys generate their own value, so
// value is empty for them. ttl is the lease ("0" or empty for none, seconds, or "15m", "8h", "7d").
func (c *Client) WriteCypher(ctx context.Context, key, value, ttl string) error {
	q := url.Values{}
	if ttl != "" {
		q.Set("ttl", ttl)
	}
	if value != "" {
		q.Set("value", value)
	}
	return c.do(ctx, http.MethodPost, cypherPath(key), q, nil, nil)
}

// DeleteCypher removes a key; one already gone is not an error.
func (c *Client) DeleteCypher(ctx context.Context, key string) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, cypherPath(key), nil, nil, nil))
}

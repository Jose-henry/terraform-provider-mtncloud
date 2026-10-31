package client

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// MOSItem is the MOS catalog item: ordering it makes the tenant's MOS keys, and ordering it again
// replaces them for the whole tenant (`S3 Folder (Operational)/CreateMOS-S3.sh`).
var MOSItem = CatalogItemRef{Code: "s32311", Names: []string{"MTN Object Storage (MOS)", "MTN Object Storage"}}

// MOSKeySet is where the tenant's MOS keys are in Cypher, and its endpoints. It holds no secret.
type MOSKeySet struct {
	Prefix          string // secret/MOS-<tenant>_<user>_
	PublicEndpoint  string
	PrivateEndpoint string
}

// MOSKeys reads where the tenant's MOS keys are, now (never cached, unlike MOSCredentials). Nil
// when the tenant has none: no access and secret key under one prefix.
func (c *Client) MOSKeys(ctx context.Context) (*MOSKeySet, error) {
	me, err := c.WhoAmI(ctx)
	if err != nil {
		return nil, err
	}
	phrase := "secret/MOS-" + MOSTenantName(me.AccountName) + "_"
	recs, err := c.ListRecords(ctx, "/api/cypher", "cyphers", url.Values{"phrase": {phrase}})
	if err != nil {
		return nil, fmt.Errorf("reading the MOS keys from Cypher: %w", err)
	}
	have := map[string]bool{}
	var keys []string // in Cypher's order, as MOSCredentials takes them
	for _, r := range recs {
		if k, _ := r["itemKey"].(string); strings.HasPrefix(k, phrase) {
			have[k] = true
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		prefix, ok := strings.CutSuffix(k, "access_key")
		if !ok || !have[prefix+"secret_key"] {
			continue
		}
		set := &MOSKeySet{Prefix: prefix}
		for suffix, dst := range map[string]*string{"public_endpoint_url": &set.PublicEndpoint, "private_endpoint_url": &set.PrivateEndpoint} {
			if !have[prefix+suffix] {
				continue
			}
			s, err := c.ReadCypher(ctx, prefix+suffix)
			if err != nil {
				return nil, fmt.Errorf("reading the MOS key %s%s from Cypher: %w", prefix, suffix, err)
			}
			*dst = strings.TrimSpace(s.Value)
		}
		return set, nil
	}
	return nil, nil
}

// OrderMOSKeys orders the MOS catalog item, which makes new keys for the whole tenant and replaces
// any it has: every other user of the old keys loses access.
func (c *Client) OrderMOSKeys(ctx context.Context) (*OrderedItem, error) {
	it, err := c.FindCatalogItem(ctx, MOSItem)
	if err != nil {
		return nil, err
	}
	return c.OrderWorkflow(ctx, it.Name, map[string]any{})
}

// ForgetMOS drops the cached MOS keys and client, so the next MOS call uses keys ordered since.
func (c *Client) ForgetMOS() {
	c.cache.forget("mos-")
}

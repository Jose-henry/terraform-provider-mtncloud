package legacy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
)

// sdkCountErr matches the SDK's error when a name matches no object or several, for example
// "found 0 Budgets for web".
var sdkCountErr = regexp.MustCompile(`^found (\d+) `)

// maxNamesShown caps the names listed in a not-found message.
const maxNamesShown = 25

// namedObject is one object from a list call: its ID and name.
type namedObject struct {
	ID   int64
	Name string
}

// nameLookupError turns the SDK's "found N <things> for <name>" error into one that says what to
// do: when nothing has the name it lists the names that exist, and when several do it gives their
// IDs. The API's name filter is exact but ignores case. It returns nil for any other error.
func nameLookupError(c *sdk.Client, err error, kind, name, path string, query ...string) diag.Diagnostics {
	if err == nil || !sdkCountErr.MatchString(err.Error()) {
		return nil
	}
	return nameLookupFailed(c, kind, name, path, query...)
}

// nameLookupFailed is the error for a name that matched no object or several.
// query is extra key, value pairs for the list, as the lookup sent them (virtual images: filterType).
func nameLookupFailed(c *sdk.Client, kind, name, path string, query ...string) diag.Diagnostics {
	objs, listErr := listNamed(c, path, query...)
	if listErr != nil {
		objs = nil
	}
	return diag.Errorf("%s", nameLookupMessage(kind, name, objs, listErr == nil))
}

// nameLookupMessage builds the message. listed is false when the list call failed, so no names
// are offered.
func nameLookupMessage(kind, name string, objs []namedObject, listed bool) string {
	var same []string
	names := make([]string, 0, len(objs))
	for _, o := range objs {
		if strings.EqualFold(o.Name, name) {
			same = append(same, strconv.FormatInt(o.ID, 10))
		}
		names = append(names, o.Name)
	}
	if len(same) > 1 {
		return fmt.Sprintf("%d %ss are named %q (IDs %s). Give `id` instead of `name` to pick one.",
			len(same), kind, name, strings.Join(same, ", "))
	}
	msg := fmt.Sprintf("No %s is named %q. Names are matched exactly (case aside).", kind, name)
	switch {
	case !listed:
		return msg
	case len(names) == 0:
		return msg + " You have none."
	}
	sort.Strings(names)
	more := ""
	if len(names) > maxNamesShown {
		more = fmt.Sprintf(", and %d more", len(names)-maxNamesShown)
		names = names[:maxNamesShown]
	}
	return msg + " Yours: " + strings.Join(names, ", ") + more + "."
}

// notFoundByID is the error for an ID that matches nothing. A data source never succeeds empty.
func notFoundByID(kind string, id int64) diag.Diagnostics {
	return diag.Errorf("No %s has ID %d. Check the ID, or look it up by `name`.", kind, id)
}

// listNamed lists the objects at path. The list key differs by kind, so it is the response's one
// array of objects with a name.
func listNamed(c *sdk.Client, path string, query ...string) ([]namedObject, error) {
	q := map[string]string{"max": "1000"}
	for i := 0; i+1 < len(query); i += 2 {
		q[query[i]] = query[i+1]
	}
	resp, err := c.Execute(&sdk.Request{Method: "GET", Path: path, QueryParams: q})
	if err != nil {
		return nil, err
	}
	return parseNamed(resp.Body)
}

// parseNamed reads the objects from a list response body.
func parseNamed(body []byte) ([]namedObject, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	for _, raw := range top { // meta is an object, so it never reads as the list
		var items []struct {
			ID   json.Number `json:"id"`
			Name *string     `json:"name"`
		}
		if json.Unmarshal(raw, &items) != nil || len(items) == 0 || items[0].Name == nil {
			continue
		}
		out := make([]namedObject, 0, len(items))
		for _, it := range items {
			id, _ := it.ID.Int64()
			n := ""
			if it.Name != nil {
				n = *it.Name
			}
			out = append(out, namedObject{ID: id, Name: n})
		}
		return out, nil
	}
	return nil, nil
}

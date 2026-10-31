package legacy

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
)

// optionSourceID returns an option's value as a whole number. The API sends it as a JSON number or
// a string depending on the source.
func optionSourceID(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n == math.Trunc(n) {
			return int64(n), true
		}
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	}
	return 0, false
}

// findOptionSourceOption picks the one option named name (any case) or, when id is set, whose value
// is id. It never returns an empty option: no match is an error that lists what exists.
func findOptionSourceOption(data *[]sdk.OptionSourceOption, kind, name string, id int) (sdk.OptionSourceOption, int64, error) {
	if name == "" && id == 0 {
		return sdk.OptionSourceOption{}, 0, fmt.Errorf("%s cannot be read without name or id", kind)
	}
	var options []sdk.OptionSourceOption
	if data != nil {
		options = *data
	}
	names := make([]string, 0, len(options))
	var unreadable []string // options whose value isn't a whole number: they can't be used here
	for _, o := range options {
		v, ok := optionSourceID(o.Value)
		if !ok {
			if strings.EqualFold(o.Name, name) {
				return sdk.OptionSourceOption{}, 0, fmt.Errorf("%s %q exists, but its value (%v) isn't a number, so it can't be used here", kind, o.Name, o.Value)
			}
			unreadable = append(unreadable, o.Name)
			continue
		}
		names = append(names, o.Name)
		if (id != 0 && v == int64(id)) || (id == 0 && strings.EqualFold(o.Name, name)) {
			return o, v, nil
		}
	}
	want := fmt.Sprintf("named %q", name)
	if id != 0 {
		want = fmt.Sprintf("with id %d", id)
	}
	if len(names) == 0 && len(unreadable) > 0 {
		sort.Strings(unreadable)
		return sdk.OptionSourceOption{}, 0, fmt.Errorf("no %s %s: the %s offered (%s) have values that aren't numbers, so they can't be read", kind, want, kind, strings.Join(unreadable, ", "))
	}
	if len(names) == 0 {
		return sdk.OptionSourceOption{}, 0, fmt.Errorf("no %s %s: none is available to this login", kind, want)
	}
	sort.Strings(names)
	return sdk.OptionSourceOption{}, 0, fmt.Errorf("no %s %s. Available: %s", kind, want, strings.Join(names, ", "))
}

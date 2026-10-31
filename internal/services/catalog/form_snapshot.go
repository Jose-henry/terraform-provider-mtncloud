package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The docs show each order form's own rules and help text. They come from a snapshot of the live
// forms (forms/<type>.json, written by TestAccCatalogForms_Snapshot), because the docs are built
// without MTN Cloud. Plan-time checks always use the live form; the drift check flags a snapshot
// that no longer matches it.

//go:embed forms/*.json
var formFiles embed.FS

// formSnap is one item's order form as read on Read (a date).
type formSnap struct {
	Item   string      `json:"item"`
	Code   string      `json:"code"`
	Read   string      `json:"read"`
	Fields []snapField `json:"fields"`
}

type snapField struct {
	Name        string   `json:"name"`
	Label       string   `json:"label,omitempty"`
	Type        string   `json:"type,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Hidden      bool     `json:"hidden,omitempty"`
	Locked      bool     `json:"locked,omitempty"`
	NoBlank     bool     `json:"no_blank,omitempty"`
	RequireOn   string   `json:"required_when,omitempty"`
	VisibleOn   string   `json:"shown_when,omitempty"`
	Default     string   `json:"default,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Placeholder string   `json:"example,omitempty"`
	Min         *int64   `json:"min,omitempty"`
	Max         *int64   `json:"max,omitempty"`
	Help        string   `json:"help,omitempty"`
	Choices     []string `json:"choices,omitempty"`
}

// snapshotFor is the type's form snapshot, or nil when there is none.
func snapshotFor(typeName string) *formSnap {
	b, err := formFiles.ReadFile("forms/" + typeName + ".json")
	if err != nil {
		return nil
	}
	var s formSnap
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &s
}

func (s *formSnap) field(name string) *snapField {
	if s == nil {
		return nil
	}
	for i := range s.Fields {
		if s.Fields[i].Name == name {
			return &s.Fields[i]
		}
	}
	return nil
}

// facts is what the form says about a field, for an argument's description.
func (f *snapField) facts() string {
	if f == nil {
		return ""
	}
	var p []string
	switch {
	case f.Required:
		p = append(p, "required")
	case f.RequireOn != "":
		p = append(p, "required when "+shownCondition(f.RequireOn))
	}
	if f.VisibleOn != "" {
		p = append(p, "used only when "+shownCondition(f.VisibleOn))
	}
	if f.Locked {
		p = append(p, "locked (not editable)")
	}
	switch {
	case f.NoBlank && (f.Type == "select" || f.Type == "radio") && f.Default == "" && !f.Hidden:
		p = append(p, "never blank: left out, its first choice is sent, as the console shows it")
	case f.NoBlank:
		p = append(p, "not blank")
	}
	if f.Default != "" && !strings.Contains(f.Default, "${") && !strings.HasPrefix(f.Default, "[") {
		p = append(p, fmt.Sprintf("pre-filled with `%s`", f.Default))
	}
	if f.Min != nil || f.Max != nil {
		p = append(p, fmt.Sprintf("from %s to %s", bound(f.Min, "any"), bound(f.Max, "any")))
	}
	if f.Pattern != "" {
		p = append(p, fmt.Sprintf("must match `%s`", f.Pattern))
	}
	if f.Placeholder != "" && f.Type != "diskManager" && f.Type != "networkManager" {
		p = append(p, fmt.Sprintf("for example `%s`", f.Placeholder))
	}
	if len(f.Choices) > 0 {
		p = append(p, "choices: "+quoteAll(f.Choices))
	}
	out := ""
	if len(p) > 0 {
		out = " Order form: " + strings.Join(p, "; ") + "."
	}
	if h := strings.Join(strings.Fields(f.Help), " "); h != "" {
		out += " It says: \"" + strings.TrimSuffix(h, ".") + ".\""
	}
	return out
}

// formSummary lists, for the resource's description, the fields the form fills itself and those no
// argument covers.
func (r *orderResource) formSummary() string {
	s := snapshotFor(r.spec.typeName)
	if s == nil {
		return ""
	}
	known := map[string]bool{"cloud": true}
	for _, f := range r.spec.fields {
		known[f.key] = true
	}
	var hidden, other []string
	for _, f := range s.Fields {
		switch {
		case known[f.Name]:
		case f.Hidden:
			hidden = append(hidden, "`"+f.Name+"`")
		default:
			other = append(other, "`"+f.Name+"`")
		}
	}
	slices.Sort(hidden)
	slices.Sort(other)
	out := fmt.Sprintf(" Each argument says what the %s order form requires and allows (read %s); every plan checks the live form.", s.Item, s.Read)
	if len(hidden) > 0 {
		out += " The form fills " + strings.Join(hidden, ", ") + " itself, and they are sent as it fills them."
	}
	if len(other) > 0 {
		out += " Its other fields, which no argument sets, go in `extra_inputs`: " + strings.Join(other, ", ") + "."
	}
	return out
}

func quoteAll(v []string) string {
	q := make([]string, len(v))
	for i, s := range v {
		q[i] = "`" + s + "`"
	}
	return strings.Join(q, ", ")
}

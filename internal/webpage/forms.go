package webpage

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	enctypeURLEncoded = "application/x-www-form-urlencoded"
	enctypeMultipart  = "multipart/form-data"
)

// maxLabelRunes bounds a field's or option's label.
const maxLabelRunes = 200

// inputTypes are the input types a browser knows; any other type, or none,
// is a text field.
var inputTypes = map[string]bool{
	"text": true, "search": true, "tel": true, "url": true, "email": true, "password": true,
	"date": true, "month": true, "week": true, "time": true, "datetime-local": true,
	"number": true, "range": true, "color": true, "checkbox": true, "radio": true,
	"file": true, "submit": true, "image": true, "reset": true, "button": true, "hidden": true,
}

// formReader reads the forms of one document. It computes the text of each
// labelling node once, cut to maxLabelRunes, so a long label shared by many
// controls costs its length once, not once per control.
type formReader struct {
	*document
	texts map[*html.Node]string
}

// forms reads every <form> of the page with the controls it owns.
func (d *document) forms() []Form {
	var forms, controls []*html.Node
	order := map[*html.Node]int{}
	var visit func(n *html.Node)
	visit = func(n *html.Node) {
		if isElement(n) {
			switch n.Data {
			case "template":
				return
			case "form":
				order[n] = len(order)
				forms = append(forms, n)
			case "input", "textarea", "select", "button":
				order[n] = len(order)
				controls = append(controls, n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(d.root)
	if len(forms) == 0 {
		return nil
	}

	// emptied holds, per table, the forms the parser emptied inside it, in
	// document order.
	emptied := map[*html.Node][]*html.Node{}
	for _, f := range forms {
		if f.FirstChild == nil && isTablePart(f.Parent) {
			if table := enclosing(f, "table"); table != nil {
				emptied[table] = append(emptied[table], f)
			}
		}
	}
	owned := map[*html.Node][]*html.Node{}
	for _, c := range controls {
		if f := d.owner(c, emptied, order); f != nil {
			owned[f] = append(owned[f], c)
		}
	}
	r := &formReader{document: d, texts: map[*html.Node]string{}}
	out := make([]Form, 0, len(forms))
	for _, f := range forms {
		out = append(out, r.form(f, owned[f]))
	}
	return out
}

// owner is the form a control belongs to: the one its form attribute
// names, else the nearest enclosing form, else (the parser's table quirk)
// the last emptied form that opened earlier in the same table.
func (d *document) owner(c *html.Node, emptied map[*html.Node][]*html.Node, order map[*html.Node]int) *html.Node {
	if id, ok := attr(c, "form"); ok {
		if f := d.byID[id]; f != nil && f.Data == "form" {
			return f
		}
		return nil
	}
	for p := c.Parent; p != nil; p = p.Parent {
		if isElement(p) && p.Data == "form" {
			return p
		}
	}
	table := enclosing(c, "table")
	if table == nil {
		return nil
	}
	cands := emptied[table]
	k := sort.Search(len(cands), func(i int) bool { return order[cands[i]] > order[c] })
	if k == 0 {
		return nil
	}
	return cands[k-1]
}

// text is n's visible text cut to maxLabelRunes, computed once per node.
func (d *formReader) text(n *html.Node) string {
	if t, ok := d.texts[n]; ok {
		return t
	}
	t := cutLabel(visibleText(n))
	d.texts[n] = t
	return t
}

// cutLabel is s cut to at most maxLabelRunes runes, at a rune boundary.
func cutLabel(s string) string {
	if len(s) <= maxLabelRunes {
		return s
	}
	i := 0
	for k := range s {
		if i == maxLabelRunes {
			return s[:k]
		}
		i++
	}
	return s
}

func isTablePart(n *html.Node) bool {
	if n == nil || !isElement(n) {
		return false
	}
	switch n.Data {
	case "table", "tbody", "thead", "tfoot", "tr":
		return true
	}
	return false
}

// enclosing is the nearest ancestor element called name.
func enclosing(n *html.Node, name string) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if isElement(p) && p.Data == name {
			return p
		}
	}
	return nil
}

func (d *formReader) form(n *html.Node, controls []*html.Node) Form {
	id, _ := attr(n, "id")
	name, _ := attr(n, "name")
	f := Form{ID: id, Name: name, Method: "GET", Enctype: enctypeURLEncoded}

	action, _ := attr(n, "action")
	action = strings.TrimSpace(action)
	switch {
	case action == "" && d.page != nil:
		u := *d.page
		u.Fragment, u.RawFragment = "", ""
		f.Action = u.String()
	case action != "":
		if u, err := d.resolve(action); err == nil {
			f.Action = u.String()
		} else {
			f.Action = action
		}
	}
	if m, _ := attr(n, "method"); strings.EqualFold(strings.TrimSpace(m), "post") {
		f.Method = "POST"
	}
	if e, _ := attr(n, "enctype"); strings.EqualFold(strings.TrimSpace(e), enctypeMultipart) {
		f.Enctype = enctypeMultipart
	}
	for _, c := range controls {
		if c.Data == "input" && inputType(c) == "file" {
			f.Enctype = enctypeMultipart
		}
	}
	f.Fields = d.fields(controls)
	return f
}

// inputType is an input's type as a browser reads it.
func inputType(n *html.Node) string {
	t, _ := attr(n, "type")
	t = strings.ToLower(strings.TrimSpace(t))
	if !inputTypes[t] {
		return "text"
	}
	return t
}

// fields turns a form's controls into fields in document order; radios
// and checkboxes that share a name become one field.
func (d *formReader) fields(controls []*html.Node) []Field {
	var out []Field
	groups := map[string]int{}        // kind + name → index in out
	groupHead := map[int]*html.Node{} // index in out → first member
	allDisabled := map[int]bool{}     // index in out → no member enabled
	for _, c := range controls {
		name, _ := attr(c, "name")
		if name == "" {
			continue
		}
		kind := ""
		switch c.Data {
		case "input":
			switch t := inputType(c); t {
			case "button", "reset":
				continue
			case "submit", "image":
				kind = KindSubmit
			default:
				kind = t
			}
		case "button":
			t, _ := attr(c, "type")
			switch strings.ToLower(strings.TrimSpace(t)) {
			case "button", "reset":
				continue
			}
			kind = KindSubmit
		case "textarea":
			kind = KindTextarea
		case "select":
			kind = KindSelect
		}

		if kind == KindRadio || kind == KindCheckbox {
			key := kind + "\x00" + name
			i, ok := groups[key]
			if !ok {
				i = len(out)
				groups[key] = i
				groupHead[i] = c
				allDisabled[i] = true
				out = append(out, Field{Name: name, Kind: kind})
			}
			d.addChoice(&out[i], c)
			if !controlDisabled(c) {
				allDisabled[i] = false
			}
			continue
		}
		out = append(out, d.field(c, name, kind))
	}
	for i, c := range groupHead {
		g := &out[i]
		g.Disabled = allDisabled[i]
		g.Label = d.legend(c)
		if g.Label == "" && len(g.Options) == 1 {
			g.Label = g.Options[0].Label
		}
		for _, o := range g.Options {
			if o.Selected && !o.Disabled {
				g.Values = append(g.Values, o.Value)
			}
		}
	}
	return out
}

// addChoice adds one radio or checkbox to its group.
func (d *formReader) addChoice(g *Field, c *html.Node) {
	value, ok := attr(c, "value")
	if !ok {
		value = "on"
	}
	o := Option{Value: value, Label: d.label(c), Selected: hasAttr(c, "checked"), Disabled: controlDisabled(c)}
	if o.Selected && g.Kind == KindRadio {
		for i := range g.Options {
			g.Options[i].Selected = false
		}
	}
	g.Options = append(g.Options, o)
	if hasAttr(c, "required") {
		g.Required = true
	}
}

func (d *formReader) field(c *html.Node, name, kind string) Field {
	f := Field{
		Name:     name,
		Kind:     kind,
		Label:    d.label(c),
		Required: hasAttr(c, "required") && kind != KindHidden && kind != KindSubmit,
		Multiple: hasAttr(c, "multiple"),
		Disabled: controlDisabled(c),
	}
	if v, ok := attr(c, "maxlength"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			f.MaxLength = n
		}
	}
	f.Pattern, _ = attr(c, "pattern")

	value, _ := attr(c, "value")
	switch kind {
	case KindTextarea:
		var b strings.Builder
		for t := c.FirstChild; t != nil; t = t.NextSibling {
			if t.Type == html.TextNode {
				b.WriteString(t.Data)
			}
		}
		f.Values = []string{b.String()}
	case KindSelect:
		f.Options = selectOptions(c, f.Multiple)
		for _, o := range f.Options {
			if o.Selected && !o.Disabled {
				f.Values = append(f.Values, o.Value)
			}
		}
	case KindFile:
	case KindSubmit:
		f.Values = []string{value}
		if f.Label == "" && c.Data == "button" {
			f.Label = d.text(c)
		}
		if f.Label == "" {
			f.Label = cutLabel(collapse(value))
		}
	default:
		f.Values = []string{value}
	}
	return f
}

// selectOptions reads a select's options, optgroups flattened, with the
// selection a browser starts from.
func selectOptions(sel *html.Node, multiple bool) []Option {
	var out []Option
	var visit func(n *html.Node, groupDisabled bool)
	visit = func(n *html.Node, groupDisabled bool) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if !isElement(c) {
				continue
			}
			switch c.Data {
			case "option":
				text := visibleText(c)
				value, ok := attr(c, "value")
				if !ok {
					value = text
				}
				label, _ := attr(c, "label")
				if label == "" {
					label = text
				}
				out = append(out, Option{
					Value:    value,
					Label:    cutLabel(label),
					Selected: hasAttr(c, "selected"),
					Disabled: groupDisabled || hasAttr(c, "disabled"),
				})
			case "optgroup":
				visit(c, groupDisabled || hasAttr(c, "disabled"))
			default:
				visit(c, groupDisabled)
			}
		}
	}
	visit(sel, false)
	if multiple {
		return out
	}
	last := -1
	for i, o := range out {
		if o.Selected {
			last = i
		}
		out[i].Selected = false
	}
	if last < 0 && displaySize(sel) <= 1 {
		for i, o := range out {
			if !o.Disabled {
				last = i
				break
			}
		}
	}
	if last >= 0 {
		out[last].Selected = true
	}
	return out
}

// displaySize is a single select's size attribute, 1 when absent or bad.
func displaySize(sel *html.Node) int {
	v, ok := attr(sel, "size")
	if !ok {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// label is what the page calls a control: its <label for>, a label
// wrapping it, aria-label, aria-labelledby, else its placeholder.
func (d *formReader) label(c *html.Node) string {
	id, _ := attr(c, "id")
	if id != "" && d.byID[id] == c {
		if l := d.labelFor[id]; l != nil {
			if t := d.text(l); t != "" {
				return t
			}
		}
	}
	if l := enclosing(c, "label"); l != nil {
		if target, ok := attr(l, "for"); !ok || target == "" || target == id {
			if t := d.text(l); t != "" {
				return t
			}
		}
	}
	if v, _ := attr(c, "aria-label"); collapse(v) != "" {
		return cutLabel(collapse(v))
	}
	if v, _ := attr(c, "aria-labelledby"); v != "" {
		var b strings.Builder
		for _, ref := range strings.Fields(v) {
			if utf8.RuneCountInString(b.String()) >= maxLabelRunes {
				break
			}
			if n := d.byID[ref]; n != nil {
				if t := d.text(n); t != "" {
					if b.Len() > 0 {
						b.WriteByte(' ')
					}
					b.WriteString(t)
				}
			}
		}
		if b.Len() > 0 {
			return cutLabel(b.String())
		}
	}
	v, _ := attr(c, "placeholder")
	return cutLabel(collapse(v))
}

// legend is the text of the first <legend> of the fieldset around c.
func (d *formReader) legend(c *html.Node) string {
	fs := enclosing(c, "fieldset")
	if fs == nil {
		return ""
	}
	if l := firstLegend(fs); l != nil {
		return d.text(l)
	}
	return ""
}

func firstLegend(fieldset *html.Node) *html.Node {
	for c := fieldset.FirstChild; c != nil; c = c.NextSibling {
		if isElement(c) && c.Data == "legend" {
			return c
		}
	}
	return nil
}

// controlDisabled reports a control disabled by its own attribute or by a
// disabled fieldset around it (except inside that fieldset's first legend).
func controlDisabled(c *html.Node) bool {
	if hasAttr(c, "disabled") {
		return true
	}
	for child, p := c, c.Parent; p != nil; child, p = p, p.Parent {
		if isElement(p) && p.Data == "fieldset" && hasAttr(p, "disabled") {
			if !isElement(child) || child.Data != "legend" || child != firstLegend(p) {
				return true
			}
		}
	}
	return false
}

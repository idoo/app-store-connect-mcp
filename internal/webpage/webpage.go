// Package webpage reads an HTML page the way a person does: its title, its
// visible text, its links and its forms, and fills a form in as a browser
// would send it. It holds no network code.
package webpage

import "errors"

// Page is what an HTML page says, with no markup, scripts or styles.
type Page struct {
	Title string
	// Text is the visible text. A block element ends a line, runs of
	// white space collapse to one space, blank lines never repeat, a
	// heading starts with "# " and a list item with "- ". Text inside
	// script, style, noscript, template, svg and head is left out, and so
	// is text of elements hidden with the hidden attribute.
	Text  string
	Links []Link
	Forms []Form
	// Scripted reports an app shell: almost no visible text while the page
	// loads scripts, so its content and forms are built in the browser.
	Scripted bool
}

// Link is one <a href> with visible text. Href is absolute (resolved
// against the page's URL and its <base>), http or https only.
type Link struct {
	Text string
	Href string
}

// Form is one <form> with the controls a browser would send.
type Form struct {
	ID   string
	Name string
	// Action is absolute: the action attribute resolved against the page,
	// or the page's own URL when the form has none.
	Action string
	// Method is "GET" or "POST".
	Method string
	// Enctype is "application/x-www-form-urlencoded" or
	// "multipart/form-data" (also forced by a file control).
	Enctype string
	Fields  []Field
}

// Field kinds beyond an input's own type attribute.
const (
	KindTextarea = "textarea"
	KindSelect   = "select"
	KindRadio    = "radio"
	KindCheckbox = "checkbox"
	KindHidden   = "hidden"
	KindFile     = "file"
	KindSubmit   = "submit"
)

// Field is one named control, or one group of radios or checkboxes that
// share a name. Controls without a name are not fields.
type Field struct {
	Name string
	// Kind is the input's type ("text", "email", "tel", "url", "number",
	// "date", "password", "hidden", "file", "submit", ...) or "textarea",
	// "select", "radio", "checkbox".
	Kind string
	// Label is what the page calls the field: its <label for>, a label
	// wrapping it, aria-label, a fieldset's legend for a group, else its
	// placeholder.
	Label string
	// Caption is the short text that stands just before the control, or
	// before the outermost wrapper the control comes first in: a question
	// in a paragraph or a heading above an input. It is never merged into
	// Label, and a hidden control has none.
	Caption  string
	Required bool
	Multiple bool
	Disabled bool
	// Readonly marks a text control the page fills in itself: its value is
	// sent, and Fill refuses an answer to it.
	Readonly bool
	// Values are the field's defaults as a browser would send them
	// untouched: the value attribute, a textarea's text, the selected
	// options, the checked boxes.
	Values []string
	// Options are a select's options, a radio group's or a checkbox
	// group's choices.
	Options   []Option
	MaxLength int
	Pattern   string
}

// Option is one choice of a select, a radio group or a checkbox group.
type Option struct {
	Value    string
	Label    string
	Selected bool
	Disabled bool
}

// Answers is what to put into a form. A name absent from Values keeps the
// field's defaults; a present name replaces them (an empty slice clears a
// checkbox group or unticks a box).
type Answers struct {
	Values map[string][]string
	Files  map[string]File
	// Submitter names the submit control to send with the form; "" sends
	// none. When several submit controls share the name and differ in
	// value, "name=value" picks one.
	Submitter string
}

// File is one file for a file control.
type File struct {
	Name string
	MIME string
	Data []byte
}

// Pair is one name and value as sent, in the form's own order. A file's
// value is its name and File is set.
type Pair struct {
	Name  string
	Value string
	File  bool
	// Hidden marks a pair whose value is the page's own and is sent but
	// never shown: a hidden control (an anti-forgery token and the like)
	// or a prefilled password field, as long as the caller did not answer
	// it. An answered hidden control is an ordinary shown pair with
	// Answered set.
	Hidden bool
	// Answered marks a hidden control's pair whose value the caller gave.
	Answered bool
}

// Filled is a form ready to send.
type Filled struct {
	ContentType string
	Body        []byte
	Pairs       []Pair
}

// Problem is one reason a form can't be sent as answered.
type Problem struct {
	Field   string
	Message string
}

// ErrNotHTML reports a document with no HTML in it.
var ErrNotHTML = errors.New("webpage: not an HTML page")

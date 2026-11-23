package webpage

import (
	"bytes"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// scriptedTextLimit is the visible text under which a page that loads
// scripts counts as an app shell.
const scriptedTextLimit = 200

// Parse reads one HTML page that was served from pageURL.
func Parse(r io.Reader, pageURL string) (Page, error) {
	src, err := io.ReadAll(r)
	if err != nil {
		return Page{}, err
	}
	if !hasElement(src) {
		return Page{}, ErrNotHTML
	}
	root, err := html.Parse(bytes.NewReader(src))
	if err != nil {
		return Page{}, err
	}

	d := newDocument(root, pageURL)
	w := &textWriter{}
	w.walk(root)
	page := Page{
		Title: d.title(),
		Text:  w.String(),
		Links: d.links(),
		Forms: d.forms(),
	}
	page.Scripted = utf8.RuneCountInString(page.Text) < scriptedTextLimit && d.scriptSrc
	return page, nil
}

// hasElement reports whether the source itself opens any element; the
// parser invents html, head and body around plain text or JSON.
func hasElement(src []byte) bool {
	z := html.NewTokenizer(bytes.NewReader(src))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken, html.SelfClosingTagToken:
			return true
		}
	}
}

// document is a parsed page with what several readers need: where it was
// served from, its base URL, its elements by id and its labels by target.
type document struct {
	root      *html.Node
	page      *url.URL // nil when pageURL can't be parsed
	base      *url.URL // nil when there is nothing to resolve against
	byID      map[string]*html.Node
	labelFor  map[string]*html.Node
	scriptSrc bool
}

func newDocument(root *html.Node, pageURL string) *document {
	d := &document{root: root, byID: map[string]*html.Node{}, labelFor: map[string]*html.Node{}}
	if u, err := url.Parse(strings.TrimSpace(pageURL)); err == nil && pageURL != "" {
		d.page = u
		d.base = u
	}
	baseSeen := false
	var visit func(n *html.Node)
	visit = func(n *html.Node) {
		if isElement(n) {
			if id, ok := attr(n, "id"); ok && id != "" {
				if _, dup := d.byID[id]; !dup {
					d.byID[id] = n
				}
			}
			switch n.Data {
			case "base":
				if href, ok := attr(n, "href"); ok && !baseSeen {
					baseSeen = true
					if u, err := d.resolve(strings.TrimSpace(href)); err == nil {
						d.base = u
					}
				}
			case "script":
				if src, ok := attr(n, "src"); ok && strings.TrimSpace(src) != "" {
					d.scriptSrc = true
				}
			case "label":
				if target, ok := attr(n, "for"); ok && target != "" {
					if _, dup := d.labelFor[target]; !dup {
						d.labelFor[target] = n
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	return d
}

// resolve makes ref absolute against the base, or parses it alone when the
// page has no usable URL.
func (d *document) resolve(ref string) (*url.URL, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return nil, err
	}
	if d.base == nil {
		return u, nil
	}
	return d.base.ResolveReference(u), nil
}

func (d *document) title() string {
	if n := findFirst(d.root, "title", false); n != nil {
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
			}
		}
		if t := collapse(b.String()); t != "" {
			return t
		}
	}
	if n := findFirst(d.root, "h1", true); n != nil {
		return visibleText(n)
	}
	return ""
}

func (d *document) links() []Link {
	var out []Link
	seen := map[Link]bool{}
	var visit func(n *html.Node)
	visit = func(n *html.Node) {
		if isElement(n) {
			if skipped(n) {
				return
			}
			if n.Data == "a" {
				if l, ok := d.link(n); ok && !seen[l] {
					seen[l] = true
					out = append(out, l)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(d.root)
	return out
}

func (d *document) link(a *html.Node) (Link, bool) {
	href, ok := attr(a, "href")
	href = strings.TrimSpace(href)
	if !ok || href == "" || strings.HasPrefix(href, "#") {
		return Link{}, false
	}
	text := visibleText(a)
	if text == "" {
		return Link{}, false
	}
	u, err := d.resolve(href)
	if err != nil || u.Host == "" {
		return Link{}, false
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return Link{}, false
	}
	return Link{Text: text, Href: u.String()}, true
}

// findFirst returns the first element called name in document order;
// visibleOnly leaves out what the text rules hide.
func findFirst(n *html.Node, name string, visibleOnly bool) *html.Node {
	if isElement(n) {
		if visibleOnly && skipped(n) {
			return nil
		}
		if n.Data == name {
			return n
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirst(c, name, visibleOnly); found != nil {
			return found
		}
	}
	return nil
}

func isElement(n *html.Node) bool { return n.Type == html.ElementNode }

// attr returns an attribute of an element; names are lower case after
// parsing.
func attr(n *html.Node, name string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == name {
			return a.Val, true
		}
	}
	return "", false
}

func hasAttr(n *html.Node, name string) bool {
	_, ok := attr(n, name)
	return ok
}

// skipped reports an element whose content is not page text: code, style,
// vector art, the head, a form control's own text, and anything hidden.
func skipped(n *html.Node) bool {
	switch n.Data {
	case "script", "style", "noscript", "template", "svg", "head", "iframe",
		"textarea", "select", "button", "option", "datalist":
		return true
	}
	if hasAttr(n, "hidden") {
		return true
	}
	v, _ := attr(n, "aria-hidden")
	return strings.EqualFold(strings.TrimSpace(v), "true")
}

// Block levels of an element in the text.
const (
	inline    = iota
	lineBlock // ends the line before and after
	paraBlock // also stands apart by a blank line
)

func blockLevel(name string) int {
	switch name {
	case "p", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "dl", "table", "pre", "blockquote":
		return paraBlock
	case "html", "body", "div", "li", "tr", "section", "article", "header", "footer", "nav", "main",
		"aside", "form", "fieldset", "legend", "address", "figure", "figcaption", "dt", "dd",
		"details", "summary", "caption", "thead", "tbody", "tfoot", "menu", "dialog", "hgroup", "search":
		return lineBlock
	}
	return inline
}

// linePrefix is what starts the line of a heading or a list item.
func linePrefix(name string) string {
	switch name {
	case "h1":
		return "# "
	case "h2":
		return "## "
	case "h3", "h4", "h5", "h6":
		return "### "
	case "li":
		return "- "
	}
	return ""
}

// textWriter lays visible text out in lines.
type textWriter struct {
	lines     []string
	cur       strings.Builder
	space     bool   // white space seen since the last character of cur
	wantBlank bool   // a blank line goes before the next line
	prefix    string // starts the next line that gets text
	plain     bool   // no heading or list prefixes
}

func (w *textWriter) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		w.text(n.Data)
		return
	case html.ElementNode:
	default:
		w.children(n)
		return
	}
	if skipped(n) {
		return
	}
	switch n.Data {
	case "br":
		w.lineBreak()
		return
	case "hr":
		w.blank()
		return
	case "img":
		if alt, _ := attr(n, "alt"); collapse(alt) != "" {
			w.text("[" + collapse(alt) + "]")
		}
		return
	}

	level := blockLevel(n.Data)
	w.boundary(level)
	prefix := ""
	if !w.plain {
		prefix = linePrefix(n.Data)
	}
	if prefix != "" {
		w.prefix = prefix
	}
	w.children(n)
	if n.Data == "td" || n.Data == "th" {
		w.space = true
	}
	w.boundary(level)
	if prefix != "" && w.prefix == prefix {
		w.prefix = ""
	}
}

func (w *textWriter) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c)
	}
}

func (w *textWriter) boundary(level int) {
	switch level {
	case lineBlock:
		w.endLine()
	case paraBlock:
		w.blank()
	}
}

func (w *textWriter) text(s string) {
	for _, r := range s {
		if isSpace(r) {
			w.space = true
			continue
		}
		if w.cur.Len() == 0 {
			w.cur.WriteString(w.prefix)
			w.prefix = ""
		} else if w.space {
			w.cur.WriteByte(' ')
		}
		w.space = false
		w.cur.WriteRune(r)
	}
}

// endLine closes the current line; an empty one leaves no trace.
func (w *textWriter) endLine() {
	w.space = false
	if w.cur.Len() == 0 {
		return
	}
	if w.wantBlank && len(w.lines) > 0 {
		w.lines = append(w.lines, "")
	}
	w.wantBlank = false
	w.lines = append(w.lines, w.cur.String())
	w.cur.Reset()
}

// blank closes the line and asks for one blank line before the next.
func (w *textWriter) blank() {
	w.endLine()
	w.wantBlank = true
}

// lineBreak is a <br>: it ends the line, and on an empty line leaves a
// blank one.
func (w *textWriter) lineBreak() {
	if w.cur.Len() == 0 {
		w.wantBlank = true
		return
	}
	w.endLine()
}

func (w *textWriter) String() string {
	w.endLine()
	return strings.Join(w.lines, "\n")
}

// visibleText is the visible text inside n on one line.
func visibleText(n *html.Node) string {
	w := &textWriter{plain: true}
	w.children(n)
	w.endLine()
	parts := w.lines[:0]
	for _, l := range w.lines {
		if l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

func isSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\f', '\u00a0':
		return true
	}
	return false
}

// collapse trims s and turns every run of white space into one space.
func collapse(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if isSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

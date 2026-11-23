package developer

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"net/url"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/webpage"
)

const (
	pageTextMax  = 12000
	pageLinksMax = 80
	scriptedHint = "the page builds its content in the browser, so its text and forms are mostly missing here; " +
		"its data comes from services-account calls: read them with developer_api_get"
	notHTMLHint = "not an HTML page; its body is not shown here (JSON lives behind developer_api_get)"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "developer_page_get", Toolset: tool.Developer, Class: tool.Read, Idempotent: true,
		Title: "Read a developer account page",
		Description: "Read a developer.apple.com account or contact page with the Apple ID sign-in (web_login): title, " +
			"text paged by offset, links, forms with their fields (hidden values never shown), as third-party data. " +
			"Send a form: developer_form_submit.",
		Ranges: map[string][2]int{"offset": {0, 100_000_000}},
	}, pageGet))
}

type pageGetIn struct {
	Path   string         `json:"path" jsonschema:"under https://developer.apple.com/, starting account/ or contact/; a full URL on that host works"`
	Query  map[string]any `json:"query,omitempty" jsonschema:"values a string or a list of strings"`
	Offset int            `json:"offset,omitempty" jsonschema:"characters into the text (next_offset)"`
}

type optionOut struct {
	Value    string         `json:"value"`
	Label    tool.Untrusted `json:"label,omitempty"`
	Selected bool           `json:"selected,omitempty"`
}

type fieldOut struct {
	Name      string           `json:"name"`
	Kind      string           `json:"kind"`
	Label     tool.Untrusted   `json:"label,omitempty"`
	Required  bool             `json:"required,omitempty"`
	Multiple  bool             `json:"multiple,omitempty"`
	Disabled  bool             `json:"disabled,omitempty"`
	Value     *tool.Untrusted  `json:"value,omitempty"`
	Values    []tool.Untrusted `json:"values,omitempty"`
	Options   []optionOut      `json:"options,omitempty"`
	MaxLength int              `json:"max_length,omitempty"`
}

type formOut struct {
	Index   int        `json:"index"`
	ID      string     `json:"id,omitempty"`
	Name    string     `json:"name,omitempty"`
	Action  string     `json:"action"`
	Method  string     `json:"method"`
	Enctype string     `json:"enctype"`
	Fields  []fieldOut `json:"fields"`
}

type linkOut struct {
	Text tool.Untrusted `json:"text"`
	Href string         `json:"href"`
}

type pageOut struct {
	URL         string         `json:"url"`
	Status      int            `json:"status"`
	ContentType string         `json:"content_type,omitempty"`
	Bytes       int            `json:"bytes,omitempty"`
	Title       tool.Untrusted `json:"title,omitempty"`
	Text        tool.Untrusted `json:"text"`
	TextTotal   int            `json:"text_total"`
	NextOffset  int            `json:"next_offset,omitempty"`
	Scripted    bool           `json:"scripted,omitempty"`
	Hint        string         `json:"hint,omitempty"`
	Forms       []formOut      `json:"forms"`
	Links       []linkOut      `json:"links"`
	LinksTotal  int            `json:"links_total,omitempty"`
}

// pageInput cleans a page path plus its query argument.
func pageInput(path string, query map[string]any) (string, url.Values, error) {
	p, q, err := cleanPagePath(path)
	if err != nil {
		return "", nil, err
	}
	extra, err := values("query", query)
	if err != nil {
		return "", nil, err
	}
	for k, vs := range extra {
		q[k] = append(q[k], vs...)
	}
	return p, q, nil
}

// answerURL is the URL that answered, else the asked one.
func answerURL(a tool.DevSiteAnswer, path string, q url.Values) string {
	if a.URL != "" {
		return a.URL
	}
	u := siteOrigin + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// isHTML reports an HTML content type (or none, for a sniffable body).
func isHTML(contentType string) bool {
	if contentType == "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && (mt == "text/html" || mt == "application/xhtml+xml")
}

// parsePage parses an HTML answer; ok is false for anything that is not
// HTML.
func parsePage(a tool.DevSiteAnswer, pageURL string) (webpage.Page, bool, error) {
	if !isHTML(a.ContentType) {
		return webpage.Page{}, false, nil
	}
	p, err := webpage.Parse(bytes.NewReader(a.Body), pageURL)
	if errors.Is(err, webpage.ErrNotHTML) {
		return webpage.Page{}, false, nil
	}
	if err != nil {
		return webpage.Page{}, false, err
	}
	return p, true, nil
}

func pageGet(ctx context.Context, env *tool.Env, in pageGetIn) (*tool.Result, error) {
	path, q, err := pageInput(in.Path, in.Query)
	if err != nil {
		return nil, err
	}
	ans, err := env.DevSite.Page(ctx, path, q)
	if err != nil {
		return nil, siteErr(err)
	}
	u := answerURL(ans, path, q)
	out := pageOut{URL: u, Status: ans.Status, Forms: []formOut{}, Links: []linkOut{}}
	page, ok, err := parsePage(ans, u)
	if err != nil {
		return nil, err
	}
	if !ok {
		out.ContentType, out.Bytes = ans.ContentType, len(ans.Body)
		out.Hint = notHTMLHint
		return tool.OK(out), nil
	}
	text := []rune(page.Text)
	out.Title, out.TextTotal = tool.Untrusted(page.Title), len(text)
	start := min(in.Offset, len(text))
	end := min(start+pageTextMax, len(text))
	out.Text = tool.Untrusted(string(text[start:end]))
	if end < len(text) {
		out.NextOffset = end
	}
	if page.Scripted {
		out.Scripted, out.Hint = true, scriptedHint
	}
	for i, f := range page.Forms {
		out.Forms = append(out.Forms, describeForm(i, f))
	}
	out.Links, out.LinksTotal = pickLinks(page.Links)
	return tool.OK(out), nil
}

// describeForm lists a form's fields; a hidden or password field is named
// with its kind only, never its value. The page's ids, names, actions and
// option values are sanitised onto one bounded line.
func describeForm(i int, f webpage.Form) formOut {
	fo := formOut{
		Index: i, ID: tool.SanitizeLine(f.ID, idMax), Name: tool.SanitizeLine(f.Name, idMax),
		Action: tool.SanitizeLine(f.Action, valueMax), Method: f.Method, Enctype: f.Enctype, Fields: []fieldOut{},
	}
	for _, fd := range f.Fields {
		o := fieldOut{Name: tool.SanitizeLine(fd.Name, idMax), Kind: fd.Kind}
		if webpage.SecretKind(fd.Kind) {
			fo.Fields = append(fo.Fields, o)
			continue
		}
		o.Label, o.Required, o.Multiple, o.Disabled, o.MaxLength = tool.Untrusted(fd.Label), fd.Required, fd.Multiple, fd.Disabled, fd.MaxLength
		group := fd.Multiple || fd.Kind == webpage.KindCheckbox
		switch {
		case len(fd.Values) == 1 && !group:
			v := tool.Untrusted(fd.Values[0])
			o.Value = &v
		case len(fd.Values) > 0:
			for _, v := range fd.Values {
				o.Values = append(o.Values, tool.Untrusted(v))
			}
		}
		for _, op := range fd.Options {
			if !op.Disabled {
				o.Options = append(o.Options, optionOut{Value: tool.SanitizeLine(op.Value, valueMax), Label: tool.Untrusted(op.Label), Selected: op.Selected})
			}
		}
		fo.Fields = append(fo.Fields, o)
	}
	return fo
}

// pickLinks keeps at most pageLinksMax links, developer.apple.com first.
func pickLinks(links []webpage.Link) ([]linkOut, int) {
	out := []linkOut{}
	for _, own := range []bool{true, false} {
		for _, l := range links {
			if len(out) == pageLinksMax {
				return out, len(links)
			}
			if onSite(l.Href) == own {
				out = append(out, linkOut{Text: tool.Untrusted(l.Text), Href: tool.SanitizeLine(l.Href, valueMax)})
			}
		}
	}
	return out, len(links)
}

func onSite(href string) bool {
	u, err := url.Parse(href)
	return err == nil && strings.EqualFold(u.Host, siteHost)
}

package developer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/webpage"
)

const (
	fileMax         = 10 << 20
	answerTextMax   = 4000
	sameFormRefusal = "the answer shows the form again: Apple usually does this when it refused the input; read text for the reason"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "developer_form_submit", Toolset: tool.Developer, Class: tool.Destructive, Confirm: tool.ConfirmByPlan,
		Title: "Submit a developer account form",
		Description: "Send a form of a developer.apple.com account or contact page (Apple's request forms) with the Apple " +
			"ID sign-in (web_login). Without plan_id fills it and previews every value; with plan_id sends it once after " +
			"human approval. Read the page first: developer_page_get.",
	}, formSubmit))
}

type formPlanIn struct {
	Path      string            `json:"path" jsonschema:"page the form stands on, as developer_page_get takes it"`
	Query     map[string]any    `json:"query,omitempty" jsonschema:"page query; values a string or a list of strings"`
	Form      string            `json:"form,omitempty" jsonschema:"form index, id or name; optional when the page has one POST form"`
	Fields    map[string]any    `json:"fields,omitempty" jsonschema:"field name → string or list of strings; others keep the page's defaults"`
	Files     map[string]string `json:"files,omitempty" jsonschema:"file field name → file path under the root (at most 10 MiB)"`
	Submitter string            `json:"submitter,omitempty" jsonschema:"name of the submit control to send"`
}

type formSubmitIn struct {
	tool.ConfirmPlan
	formPlanIn
}

// filledForm is a page's form filled from the arguments, ready to send.
// pagePath and pageQuery are the page the form stands on.
type filledForm struct {
	pagePath    string
	pageQuery   url.Values
	title       string
	form        webpage.Form
	actionPath  string
	actionQuery url.Values
	filled      webpage.Filled
	files       map[string]webpage.File
}

type problemsBody struct {
	tool.ErrorBody
	Problems []problemOut `json:"problems"`
	Fields   []string     `json:"fields"`
}

type problemOut struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func formSubmit(ctx context.Context, env *tool.Env, in formSubmitIn) (*tool.Result, error) {
	files, err := readFiles(env, in.Files)
	if err != nil {
		return nil, err
	}
	ff, res, err := fillForm(ctx, env, in.formPlanIn, files)
	if err != nil || res != nil {
		return res, err
	}
	fp, err := tool.Fingerprint(ff.state())
	if err != nil {
		return nil, err
	}
	wctx, gate := tool.PreviewHuman(ctx, env, in.PlanID, in.formPlanIn, fp, ff.preview())
	if gate != nil {
		if in.PlanID == "" && !gate.IsError {
			if m := asMap(gate.Data); m != nil {
				if id, _ := m["plan_id"].(string); id != "" {
					gate.Then(tool.NextStep{Tool: "developer_form_submit", Args: planArgs(in.formPlanIn, id)})
				}
			}
		}
		return gate, nil
	}
	ans, err := env.DevSite.Submit(wctx, tool.DevSitePost{
		Path: ff.actionPath, Query: ff.actionQuery, Referer: ff.pagePath, RefererQuery: ff.pageQuery,
		ContentType: ff.filled.ContentType, Body: ff.filled.Body,
	})
	if errors.Is(err, tool.ErrOutcomeUnknown) {
		return tool.OK(map[string]any{
			"outcome": "outcome_unknown",
			"hint": "Apple may or may not have received the form; it was not resent. Check its effect " +
				"(developer_page_get on the page or the account's list) before anything else.",
		}).Warn(tool.BodyFor(err).Message), nil
	}
	if err != nil {
		return nil, siteErr(err)
	}
	return tool.OK(map[string]any{
		"status": ans.Status, "outcome": "submitted", "answer": readAnswer(ctx, env, ff, ans),
	}), nil
}

// readFiles reads the files to attach, each under the root and the cap.
func readFiles(env *tool.Env, in map[string]string) (map[string]webpage.File, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]webpage.File, len(in))
	for _, name := range slices.Sorted(maps.Keys(in)) {
		p := in[name]
		st, err := env.Files.Stat(p)
		if err != nil {
			return nil, tool.InvalidArgs("files.%s: %s", name, tool.BodyFor(err).Message)
		}
		if st.IsDir() || st.Size() > fileMax {
			return nil, tool.InvalidArgs("files.%s must be a file of at most %d MiB", name, fileMax>>20)
		}
		data, err := env.Files.ReadFile(p)
		if err != nil {
			return nil, tool.InvalidArgs("files.%s: %s", name, tool.BodyFor(err).Message)
		}
		if len(data) > fileMax {
			return nil, tool.InvalidArgs("files.%s must be a file of at most %d MiB", name, fileMax>>20)
		}
		typ := mime.TypeByExtension(filepath.Ext(p))
		if typ == "" {
			typ = "application/octet-stream"
		}
		out[name] = webpage.File{Name: filepath.Base(p), MIME: typ, Data: data}
	}
	return out, nil
}

// fillForm fetches the page, picks the form and fills it. A result is an
// error answer the handler returns as is.
func fillForm(ctx context.Context, env *tool.Env, in formPlanIn, files map[string]webpage.File) (*filledForm, *tool.Result, error) {
	path, q, err := pageInput(in.Path, in.Query)
	if err != nil {
		return nil, nil, err
	}
	answers := webpage.Answers{Values: map[string][]string{}, Files: files, Submitter: in.Submitter}
	for _, k := range slices.Sorted(maps.Keys(in.Fields)) {
		vs, err := stringList("fields."+k, in.Fields[k])
		if err != nil {
			return nil, nil, err
		}
		answers.Values[k] = vs
	}
	ans, err := env.DevSite.Page(ctx, path, q)
	if err != nil {
		return nil, nil, siteErr(err)
	}
	page, ok, err := parsePage(ans, answerURL(ans, path, q))
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, tool.InvalidArgs("%s is not an HTML page", path)
	}
	form, err := pickForm(page.Forms, in.Form)
	if err != nil {
		return nil, nil, err
	}
	if form.Method != http.MethodPost {
		err := tool.InvalidArgs("this form is sent with GET: it only reads; open its action with developer_page_get and the fields as query")
		// The action is the page's own text: it is offered as a next step
		// only when it is a page this toolset serves.
		if p, aq, perr := cleanPagePath(form.Action); perr == nil {
			args := map[string]any{"path": p}
			if len(aq) > 0 {
				args["path"] = p + "?" + aq.Encode()
			}
			return nil, nil, tool.WithNext(err, tool.NextStep{Tool: "developer_page_get", Args: args})
		}
		return nil, nil, err
	}
	actionPath, actionQuery, err := actionTarget(form.Action)
	if err != nil {
		return nil, nil, err
	}
	filled, problems := form.Fill(answers)
	if len(problems) > 0 {
		return nil, problemResult(form, problems), nil
	}
	return &filledForm{
		pagePath: path, pageQuery: q, title: page.Title, form: form,
		actionPath: actionPath, actionQuery: actionQuery, filled: filled, files: files,
	}, nil, nil
}

// pickForm finds the form by index, id or name, or the page's only POST
// form.
func pickForm(forms []webpage.Form, ref string) (webpage.Form, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		var posts []webpage.Form
		for _, f := range forms {
			if f.Method == http.MethodPost {
				posts = append(posts, f)
			}
		}
		if len(posts) == 1 {
			return posts[0], nil
		}
		return webpage.Form{}, tool.InvalidArgs("the page has %d POST forms; pass form (index, id or name) from: %s", len(posts), formList(forms))
	}
	if i, err := strconv.Atoi(ref); err == nil && i >= 0 && i < len(forms) {
		return forms[i], nil
	}
	for _, f := range forms {
		if f.ID == ref {
			return f, nil
		}
	}
	for _, f := range forms {
		if f.Name == ref {
			return f, nil
		}
	}
	return webpage.Form{}, tool.InvalidArgs("no form %s on the page; forms: %s", tool.SanitizeLine(ref, 80), formList(forms))
}

func formList(forms []webpage.Form) string {
	if len(forms) == 0 {
		return "none"
	}
	parts := make([]string, len(forms))
	for i, f := range forms {
		parts[i] = fmt.Sprintf("%d id=%s name=%s %s", i, tool.SanitizeLine(f.ID, 60), tool.SanitizeLine(f.Name, 60), f.Method)
	}
	return strings.Join(parts, "; ")
}

// actionTarget checks a form's action stays on the developer site's page
// roots and makes it relative.
func actionTarget(action string) (string, url.Values, error) {
	u, err := url.Parse(action)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, siteHost) || u.User != nil {
		return "", nil, tool.InvalidArgs("the form posts off %s (%s); refused", siteHost, tool.SanitizeLine(action, 200))
	}
	p := strings.Trim(u.Path, "/")
	if cleanPathSegments(p, "action") != nil || !inRoots(p) {
		return "", nil, tool.InvalidArgs("the form posts outside %s's %s pages (%s); refused",
			siteHost, strings.Join(tool.DevSitePageRoots, "/"), tool.SanitizeLine(action, 200))
	}
	return p, u.Query(), nil
}

func problemResult(form webpage.Form, problems []webpage.Problem) *tool.Result {
	b := problemsBody{
		ErrorBody: tool.ErrorBody{
			Error:   tool.ErrInvalidArgs.Code,
			Message: fmt.Sprintf("the form cannot be sent as answered (%d problems)", len(problems)),
			Hint:    "fix every listed field (names in fields) and call again without plan_id",
		},
		Fields: []string{},
	}
	for _, p := range problems {
		b.Problems = append(b.Problems, problemOut{Field: tool.SanitizeLine(p.Field, idMax), Message: tool.SanitizeLine(p.Message, 300)})
	}
	for _, f := range form.Fields {
		if name := tool.SanitizeLine(f.Name, idMax); !slices.Contains(b.Fields, name) {
			b.Fields = append(b.Fields, name)
		}
	}
	return &tool.Result{Data: b, IsError: true}
}

// state is what the plan stands on: target, encoding, every shown pair (a
// hidden control the caller answered included), each file's checksum and
// the names (not values) of the pairs that carry the page's own unseen
// value, so a fresh anti-forgery token keeps the plan while any visible
// change stales it.
func (f *filledForm) state() any {
	var pairs, files [][2]string
	hidden := []string{}
	for _, p := range f.filled.Pairs {
		switch {
		case p.Hidden:
			hidden = append(hidden, p.Name)
		case p.File:
			sum := ""
			if file, ok := f.files[p.Name]; ok {
				h := sha256.Sum256(file.Data)
				sum = hex.EncodeToString(h[:])
			}
			files = append(files, [2]string{p.Name, sum})
		default:
			pairs = append(pairs, [2]string{p.Name, p.Value})
		}
	}
	slices.Sort(hidden)
	return map[string]any{
		"action": f.actionPath + "?" + f.actionQuery.Encode(), "method": f.form.Method, "enctype": f.form.Enctype,
		"pairs": pairs, "files": files, "hidden": hidden,
	}
}

// pageRef is the page the form stands on, query included, for people.
func (f *filledForm) pageRef() string {
	if len(f.pageQuery) == 0 {
		return f.pagePath
	}
	return f.pagePath + "?" + f.pageQuery.Encode()
}

// actionURL is the absolute URL the form is posted to, query included.
func (f *filledForm) actionURL() string {
	u := siteOrigin + f.actionPath
	if len(f.actionQuery) > 0 {
		u += "?" + f.actionQuery.Encode()
	}
	return u
}

// untrustedEntryMax is how many runes of one untrusted entry an approval
// prompt shows ([tool.ApprovalPrompt] cuts a longer one).
const untrustedEntryMax = 400

// packLines joins lines with " | " into entries of at most maxRunes runes,
// in order, so a form's dozen captions cost a few prompt blocks, not a dozen.
// A line longer than maxRunes stands alone.
func packLines(lines []string, maxRunes int) []string {
	var out []string
	cur, n := "", 0
	for _, l := range lines {
		ln := utf8.RuneCountInString(l)
		switch {
		case cur == "":
			cur, n = l, ln
		case n+3+ln <= maxRunes:
			cur, n = cur+" | "+l, n+3+ln
		default:
			out = append(out, cur)
			cur, n = l, ln
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// preview shows every pair that is sent: a shown value whole as
// "<name>: <value>", a file by name and size, and the pairs that carry the
// page's own unseen value (hidden controls, a prefilled password) by name
// only. What the page says about a field (its caption and label) is page
// text and goes to Untrusted, packed into few entries.
func (f *filledForm) preview() tool.PlanPreview {
	labels, captions := map[string]string{}, map[string]string{}
	for _, fd := range f.form.Fields {
		labels[fd.Name] = strings.TrimSpace(fd.Label)
		captions[fd.Name] = strings.TrimSpace(fd.Caption)
	}
	var intended, hidden, untrusted []string
	if f.title != "" {
		untrusted = append(untrusted, "page title: "+f.title)
	}
	described := map[string]bool{}
	for _, p := range f.filled.Pairs {
		if p.Hidden {
			hidden = append(hidden, p.Name)
			continue
		}
		if !described[p.Name] {
			described[p.Name] = true
			if c := captions[p.Name]; c != "" {
				untrusted = append(untrusted, "caption of "+p.Name+": "+c)
			}
			if l := labels[p.Name]; l != "" {
				untrusted = append(untrusted, "label of "+p.Name+": "+l)
			}
		}
		switch {
		case p.File:
			size := 0
			if file, ok := f.files[p.Name]; ok {
				size = len(file.Data)
			}
			intended = append(intended, fmt.Sprintf("file %s: %s (%d bytes)", p.Name, p.Value, size))
		case p.Answered:
			intended = append(intended, p.Name+" (hidden field, value given by this call): "+p.Value)
		default:
			intended = append(intended, p.Name+": "+p.Value)
		}
	}
	if len(hidden) > 0 {
		intended = append(intended, "hidden fields (sent, not shown): "+strings.Join(hidden, ", "))
	}
	return tool.PlanPreview{
		Summary:      fmt.Sprintf("submit the form of %s to %s%s", f.pageRef(), siteOrigin, f.actionPath),
		Intended:     intended,
		Affected:     []string{"POST " + f.actionURL()},
		Irreversible: []string{"Apple receives the form from the signed-in account; a sent request cannot be withdrawn here"},
		Untrusted:    packLines(untrusted, untrustedEntryMax),
	}
}

func planArgs(in formPlanIn, planID string) map[string]any {
	args := map[string]any{"path": in.Path, "plan_id": planID}
	if in.Query != nil {
		args["query"] = in.Query
	}
	if in.Form != "" {
		args["form"] = in.Form
	}
	if in.Fields != nil {
		args["fields"] = in.Fields
	}
	if in.Files != nil {
		args["files"] = in.Files
	}
	if in.Submitter != "" {
		args["submitter"] = in.Submitter
	}
	return args
}

// asMap re-decodes a preview body as a JSON object (nil when it isn't one).
func asMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// readAnswer describes what Apple answered the post: a page is parsed, a
// redirect inside the page roots is read once, and an answer that is not
// HTML is described by type and size only. Nothing is resent.
func readAnswer(ctx context.Context, env *tool.Env, ff *filledForm, ans tool.DevSiteAnswer) map[string]any {
	out := map[string]any{"status": ans.Status}
	if ans.Status >= 300 && ans.Status < 400 && ans.Location != "" {
		base, _ := url.Parse(siteOrigin + ff.actionPath)
		loc, err := base.Parse(ans.Location)
		if err != nil {
			out["location"] = tool.SanitizeLine(ans.Location, valueMax)
			return out
		}
		out["location"] = tool.SanitizeLine(loc.String(), valueMax)
		path, q, err := cleanPagePath(loc.String())
		if err != nil {
			out["hint"] = "the redirect leaves the developer site's pages; it was not followed"
			return out
		}
		page, err := env.DevSite.Page(ctx, path, q)
		if err != nil {
			out["hint"] = "the redirect target could not be read: " + tool.BodyFor(err).Message
			return out
		}
		out["status"] = page.Status
		ans = page
		if ans.URL == "" {
			ans.URL = loc.String()
		}
	}
	u := ans.URL
	if u == "" {
		u = siteOrigin + ff.actionPath
	}
	page, ok, err := parsePage(ans, u)
	if err != nil || !ok {
		if ans.ContentType != "" {
			out["content_type"] = ans.ContentType
		}
		out["bytes"] = len(ans.Body)
		out["hint"] = notHTMLHint
		return out
	}
	if page.Title != "" {
		out["title"] = tool.Untrusted(page.Title)
	}
	if page.Text != "" {
		out["text"] = tool.Untrusted(cut(page.Text, answerTextMax))
	}
	var again []string
	for i, f := range page.Forms {
		if p, _, err := actionTarget(f.Action); err == nil && p == ff.actionPath {
			again = append(again, tool.SanitizeLine(formRef(i, f), idMax))
		}
	}
	if len(again) > 0 {
		out["forms_with_errors"] = again
		out["hint"] = sameFormRefusal
	}
	return out
}

func formRef(i int, f webpage.Form) string {
	switch {
	case f.ID != "":
		return f.ID
	case f.Name != "":
		return f.Name
	}
	return strconv.Itoa(i)
}

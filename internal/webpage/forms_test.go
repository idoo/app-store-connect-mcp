package webpage

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
)

func onlyForm(t *testing.T, src string) Form {
	t.Helper()
	p := mustParse(t, src)
	if len(p.Forms) != 1 {
		t.Fatalf("got %d forms, want 1", len(p.Forms))
	}
	return p.Forms[0]
}

func TestParseFormAttributes(t *testing.T) {
	tests := []struct {
		name, html string
		want       Form
	}{
		{"defaults", `<form id=f name=n></form>`, Form{
			ID: "f", Name: "n",
			Action: "https://developer.apple.com/account/resources/page?x=1", Method: "GET", Enctype: enctypeURLEncoded,
		}},
		{"relative action, post", `<form action="save.action" method="Post"></form>`, Form{
			Action: "https://developer.apple.com/account/resources/save.action", Method: "POST", Enctype: enctypeURLEncoded,
		}},
		{
			"action against base", `<base href="https://developer.apple.com/services-account/QH65B2/"><form action="account/x.action" method=dialog></form>`,
			Form{Action: "https://developer.apple.com/services-account/QH65B2/account/x.action", Method: "GET", Enctype: enctypeURLEncoded},
		},
		{"declared multipart", `<form action="/up" enctype="Multipart/Form-Data" method=post></form>`, Form{
			Action: "https://developer.apple.com/up", Method: "POST", Enctype: enctypeMultipart,
		}},
		{"file control forces multipart", `<form action="/up" method=post><input type=file name=f></form>`, Form{
			Action: "https://developer.apple.com/up", Method: "POST", Enctype: enctypeMultipart,
			Fields: []Field{{Name: "f", Kind: KindFile}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, onlyForm(t, tt.html)); diff != "" {
				t.Errorf("Form (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseFieldKinds(t *testing.T) {
	f := onlyForm(t, `<form>
<input name=plain value=p>
<input type=EMAIL name=mail required maxlength=40 pattern=".+@.+" placeholder="you@example.com">
<input type=weird name=odd>
<input type=hidden name=token value=secret>
<input type=password name=pw>
<input type=file name=docs multiple>
<textarea name=desc>
line one
line two</textarea>
<input name="">
<input value=nameless>
<input type=button name=btn value=B>
<input type=reset name=rst>
<button type=button name=b2>No</button>
<input type=submit name=go value=Send>
<input type=image name=img value=I alt=Pic>
<button name=b3 value=v3>Press <b>me</b></button>
<button type=bogus name=b4>Four</button>
</form>`)
	want := []Field{
		{Name: "plain", Kind: "text", Values: []string{"p"}},
		{Name: "mail", Kind: "email", Label: "you@example.com", Required: true, Values: []string{""}, MaxLength: 40, Pattern: ".+@.+"},
		{Name: "odd", Kind: "text", Values: []string{""}},
		{Name: "token", Kind: KindHidden, Values: []string{"secret"}},
		{Name: "pw", Kind: "password", Values: []string{""}},
		{Name: "docs", Kind: KindFile, Multiple: true},
		{Name: "desc", Kind: KindTextarea, Values: []string{"line one\nline two"}},
		{Name: "go", Kind: KindSubmit, Label: "Send", Values: []string{"Send"}},
		{Name: "img", Kind: KindSubmit, Label: "I", Values: []string{"I"}},
		{Name: "b3", Kind: KindSubmit, Label: "Press me", Values: []string{"v3"}},
		{Name: "b4", Kind: KindSubmit, Label: "Four", Values: []string{""}},
	}
	if diff := cmp.Diff(want, f.Fields); diff != "" {
		t.Errorf("Fields (-want +got):\n%s", diff)
	}
}

func TestParseGroups(t *testing.T) {
	f := onlyForm(t, `<form>
<fieldset><legend>Plan</legend>
<label><input type=radio name=plan value=a> A</label>
<input type=radio name=plan value=b id=pb checked><label for=pb>B</label>
<input type=radio name=plan value=c checked required>
<input type=radio name=plan value=d disabled>
</fieldset>
<input type=text name=between>
<label><input type=checkbox name=agree> I agree</label>
<input type=checkbox name=tags value=x checked>
<input type=checkbox name=tags value=y>
<input type=checkbox name=tags value=z checked disabled>
<input type=checkbox name=off disabled checked>
</form>`)
	want := []Field{
		{Name: "plan", Kind: KindRadio, Label: "Plan", Caption: "Plan", Required: true, Values: []string{"c"}, Options: []Option{
			{Value: "a", Label: "A"}, {Value: "b", Label: "B"}, {Value: "c", Selected: true}, {Value: "d", Disabled: true},
		}},
		{Name: "between", Kind: "text", Values: []string{""}},
		{Name: "agree", Kind: KindCheckbox, Label: "I agree", Options: []Option{{Value: "on", Label: "I agree"}}},
		{Name: "tags", Kind: KindCheckbox, Values: []string{"x"}, Options: []Option{
			{Value: "x", Selected: true}, {Value: "y"}, {Value: "z", Selected: true, Disabled: true},
		}},
		{Name: "off", Kind: KindCheckbox, Disabled: true, Options: []Option{{Value: "on", Selected: true, Disabled: true}}},
	}
	if diff := cmp.Diff(want, f.Fields); diff != "" {
		t.Errorf("Fields (-want +got):\n%s", diff)
	}
}

func TestParseSelect(t *testing.T) {
	tests := []struct {
		name, html string
		want       Field
	}{
		{
			"defaults to first enabled", `<select name=s><option disabled>x</option><option value=1>One</option><option>Two</option></select>`,
			Field{Name: "s", Kind: KindSelect, Values: []string{"1"}, Options: []Option{
				{Value: "x", Label: "x", Disabled: true}, {Value: "1", Label: "One", Selected: true}, {Value: "Two", Label: "Two"},
			}},
		},
		{
			"last selected wins", `<select name=s><option selected>a</option><option selected>b</option></select>`,
			Field{Name: "s", Kind: KindSelect, Values: []string{"b"}, Options: []Option{{Value: "a", Label: "a"}, {Value: "b", Label: "b", Selected: true}}},
		},
		{
			"label attribute, optgroups", `<select name=s><optgroup label=G1><option label="Short" value=1> Long  text </option></optgroup>
			<optgroup label=G2 disabled><option selected>2</option></optgroup></select>`,
			Field{Name: "s", Kind: KindSelect, Options: []Option{
				{Value: "1", Label: "Short"}, {Value: "2", Label: "2", Selected: true, Disabled: true},
			}},
		},
		{
			"multiple selects nothing by default", `<select name=s multiple required><option>a</option><option selected>b</option><option selected>c</option></select>`,
			Field{Name: "s", Kind: KindSelect, Multiple: true, Required: true, Values: []string{"b", "c"}, Options: []Option{
				{Value: "a", Label: "a"}, {Value: "b", Label: "b", Selected: true}, {Value: "c", Label: "c", Selected: true},
			}},
		},
		{
			"list box selects nothing", `<select name=s size=3><option>a</option></select>`,
			Field{Name: "s", Kind: KindSelect, Options: []Option{{Value: "a", Label: "a"}}},
		},
		{
			"empty value kept", `<select name=s><option value="">Choose</option><option>a</option></select>`,
			Field{Name: "s", Kind: KindSelect, Values: []string{""}, Options: []Option{{Value: "", Label: "Choose", Selected: true}, {Value: "a", Label: "a"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := onlyForm(t, `<form>`+tt.html+`</form>`)
			if diff := cmp.Diff([]Field{tt.want}, f.Fields); diff != "" {
				t.Errorf("Fields (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseLabels(t *testing.T) {
	f := onlyForm(t, `<form>
<label for=a>By for</label><input id=a name=a placeholder=ph>
<label>Wrapped <span>text</span> <input name=b></label>
<input name=c aria-label=" Aria  label " placeholder=ph>
<span id=l1>First</span><span id=l2>Second</span><input name=d aria-labelledby="l1 missing l2">
<input name=e placeholder="Only placeholder">
<label for=other>Not mine <input name=f id=mine></label>
<label for=g>First label</label><label for=g>Second label</label><input id=g name=g>
</form>`)
	want := map[string]string{
		"a": "By for", "b": "Wrapped text", "c": "Aria label", "d": "First Second",
		"e": "Only placeholder", "f": "", "g": "First label",
	}
	got := map[string]string{}
	for _, fd := range f.Fields {
		got[fd.Name] = fd.Label
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("labels (-want +got):\n%s", diff)
	}
}

// requestForm is Apple's request form markup (template legacy-form
// dev-form), trimmed, with a fake token.
const requestForm = `<form id="contactForm" name="contactForm" method="post" action="/contact/request/notification-service">
<input type="hidden" name="token" value="FAKE-TOKEN">
<input type="hidden" name="contact_team_id" value="ABCDE12345">
<p><strong>App Name</strong></p>
<label for="app_name"><input type="text" name="app_name" id="app_name" maxlength="100" value="" required=""></label>

<p><strong>Apple ID of App</strong></p>
<label id="app-id" for="app_id"><input type="text" name="app_id" id="app_id" maxlength="10" required=""><span class="instruction"><a href="/help/app-id">How to find this ID</a></span></label>

<p><strong>Name</strong></p>
<label for="contact_name" class="readonly"><input type="text" id="contact_name" name="contact_name" value="Verevkin Ivan" onfocus="this.blur()" readonly></label>

<p><strong>My app needs the entitlement because it:</strong></p>
<label for="request_reason"><div class="form-box">
  <label><input type="radio" name="request_reason" value="Provides an end-to-end service" required>Provides an end-to-end service</label>
  <label><input type="radio" name="request_reason" value="Provides critical alerts" required>Provides critical alerts</label>
</div></label>

<p><strong>Explain why existing APIs are not adequate for your app.</strong></p>
<label for="reason_why_not_adequate"><textarea id="reason_why_not_adequate" name="reason_why_not_adequate" maxlength="2000" required=""></textarea></label>

<!-- a comment between -->
<h3>Describe the notifications.</h3>
<label for="notifications"><textarea id="notifications" name="notifications"></textarea></label>
<input type="submit" name="submit" value="Submit">
</form>`

func TestParseRequestForm(t *testing.T) {
	type row struct {
		Caption, Label string
		Readonly       bool
	}
	want := map[string]row{
		"token":                   {},
		"contact_team_id":         {},
		"app_name":                {Caption: "App Name"},
		"app_id":                  {Caption: "Apple ID of App", Label: "How to find this ID"},
		"contact_name":            {Caption: "Name", Readonly: true},
		"request_reason":          {Caption: "My app needs the entitlement because it:"},
		"reason_why_not_adequate": {Caption: "Explain why existing APIs are not adequate for your app."},
		"notifications":           {Caption: "Describe the notifications."},
		"submit":                  {Label: "Submit"},
	}
	got := map[string]row{}
	for _, fd := range onlyForm(t, requestForm).Fields {
		got[fd.Name] = row{Caption: fd.Caption, Label: fd.Label, Readonly: fd.Readonly}
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fields (-want +got):\n%s", diff)
	}
}

func TestParseCaptionRule(t *testing.T) {
	long := strings.Repeat("é", maxLabelRunes+1)
	limit := strings.Repeat("é", maxLabelRunes)
	tests := []struct {
		name, body string // body goes inside <form>; the field is named a
		want       string
	}{
		{"previous paragraph", `<p>Question?</p><input name=a>`, "Question?"},
		{"heading", `<h2>Head</h2><textarea name=a></textarea>`, "Head"},
		{"text and comments skipped", "<p>Q</p>\n  <!-- c -->\n <input name=a>", "Q"},
		{"text between ends the search", `<p>Q</p> Your answer: <input name=a>`, ""},
		{"climb four levels", `<p>Q</p><div><div><div><span><input name=a></span></div></div></div>`, "Q"},
		{"no climb past four levels", `<p>Q</p><div><div><div><div><span><input name=a></span></div></div></div></div>`, ""},
		{"not first in its wrapper", `<p>Q</p><div><b>x</b><input name=a></div>`, "x"},
		{"sibling of a disallowed kind", `<p>Q</p><a href=/x>link</a><input name=a>`, ""},
		{"sibling is a label", `<label>L</label><input name=a>`, ""},
		{"sibling holds a control", `<div>Other <input name=b></div><input name=a>`, ""},
		{"sibling holds a label", `<div><label>L</label></div><input name=a>`, ""},
		{"sibling holds a button", `<p>Q <button type=button>x</button></p><input name=a>`, ""},
		{"empty sibling", `<p> </p><input name=a>`, ""},
		{"longer than the cap", `<p>` + long + `</p><input name=a>`, ""},
		{"exactly the cap", `<p>` + limit + `</p><input name=a>`, limit},
		{"climb stops at a fieldset", `<p>Q</p><fieldset><input name=a></fieldset>`, ""},
		{"climb stops at a section", `<p>Q</p><section><input name=a></section>`, ""},
		{"climb stops at a table row", `<p>Q</p><table><tr><td><input name=a></td></tr></table>`, ""},
		{"climb stops at the form", `<input name=a>`, ""},
		{"legend sibling", `<fieldset><legend>Plan</legend><select name=a><option>x</option></select></fieldset>`, "Plan"},
		{"dt sibling", `<dl><dt>Term</dt><dd><input name=a></dd></dl>`, "Term"},
		{"hidden control has none", `<p>Q</p><input type=hidden name=a value=v>`, ""},
		{"group takes its first member's", `<p>Pick:</p><label><input type=checkbox name=a value=1> One</label><p>Not this</p><label><input type=checkbox name=a value=2> Two</label>`, "Pick:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mustParse(t, `<body><form>`+tt.body+`</form></body>`)
			for _, fd := range p.Forms[0].Fields {
				if fd.Name != "a" {
					continue
				}
				if fd.Caption != tt.want {
					t.Errorf("Caption = %q, want %q", fd.Caption, tt.want)
				}
				if fd.Label != "" && fd.Label == fd.Caption {
					t.Errorf("caption merged into the label: %q", fd.Label)
				}
				return
			}
			t.Fatal("no field a")
		})
	}
}

func TestParseReadonly(t *testing.T) {
	f := onlyForm(t, `<form>
<input name=text readonly value=t><textarea name=area readonly>x</textarea><input type=email name=mail readonly>
<input name=plain><input type=hidden name=hid readonly><input type=checkbox name=box readonly>
<select name=sel readonly><option>a</option></select><input type=submit name=go readonly><input type=file name=up readonly>
</form>`)
	want := map[string]bool{"text": true, "area": true, "mail": true, "plain": false, "hid": false, "box": false, "sel": false, "go": false, "up": false}
	got := map[string]bool{}
	for _, fd := range f.Fields {
		got[fd.Name] = fd.Readonly
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("readonly (-want +got):\n%s", diff)
	}
}

// A caption candidate that holds controls is refused without reading its
// text: here every h<i> stands after a nested div that holds the controls
// deeper in and, at the bottom, one 200 000-rune paragraph.
func TestParseCaptionCostBounded(t *testing.T) {
	const depth = 400 // the parser refuses more than 512 open elements
	var b strings.Builder
	b.WriteString(`<form>`)
	for i := range depth {
		b.WriteString(`<div><input name=f` + strconv.Itoa(i) + `>`)
	}
	b.WriteString(`<p>` + strings.Repeat("é", 200000) + `</p><input name=last>`)
	for i := depth - 1; i >= 0; i-- {
		b.WriteString(`</div><input name=h` + strconv.Itoa(i) + `>`)
	}
	b.WriteString(`</form>`)
	src := b.String()
	var f Form
	bytes := allocated(func() { f = onlyForm(t, src) })
	t.Logf("page %d bytes, parse allocated %d bytes", len(src), bytes)
	if len(f.Fields) != 2*depth+1 {
		t.Fatalf("got %d fields, want %d", len(f.Fields), 2*depth+1)
	}
	for _, fd := range f.Fields {
		if fd.Caption != "" {
			t.Fatalf("%s has caption %.40q", fd.Name, fd.Caption)
		}
	}
	if bytes > captionCostAllocBound {
		t.Errorf("parse allocated %d bytes, want at most %d: captions must not read text per control", bytes, captionCostAllocBound)
	}
}

func TestParseLabelCut(t *testing.T) {
	long := strings.Repeat("é", 300)
	cut := strings.Repeat("é", maxLabelRunes)
	tests := []struct {
		name, html string
		want       []string // labels of the fields and of their options, in order
	}{
		{"wrapping label", `<form><label>` + long + `<input name=a></label></form>`, []string{cut}},
		{"label for", `<form><label for=a>` + long + `</label><input id=a name=a></form>`, []string{cut}},
		{"aria-label", `<form><input name=a aria-label="` + long + `"></form>`, []string{cut}},
		{
			"aria-labelledby", `<form><p id=x>` + long + `</p><p id=y>` + long + `</p><input name=a aria-labelledby="x y"></form>`,
			[]string{cut},
		},
		{"placeholder", `<form><input name=a placeholder="` + long + `"></form>`, []string{cut}},
		{"button text", `<form><button name=a>` + long + `</button></form>`, []string{cut}},
		{"submit value", `<form><input type=submit name=a value="` + long + `"></form>`, []string{cut}},
		{
			"legend and choices", `<form><fieldset><legend>` + long + `</legend>
<label>` + long + `<input type=radio name=a value=1></label><label>` + long + `<input type=radio name=a value=2></label></fieldset></form>`,
			[]string{cut, cut, cut},
		},
		{"exactly the limit", `<form><label>` + cut + `<input name=a></label></form>`, []string{cut}},
		{"short", `<form><label>Name <input name=a></label></form>`, []string{"Name"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, fd := range onlyForm(t, tt.html).Fields {
				got = append(got, fd.Label)
				for _, o := range fd.Options {
					got = append(got, o.Label)
				}
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("labels (-want +got):\n%s", diff)
			}
			for _, l := range got {
				if !utf8.ValidString(l) {
					t.Errorf("label is cut inside a rune: %q", l)
				}
			}
		})
	}
}

func TestParseLabelCostBounded(t *testing.T) {
	const inputs, labelRunes = 2000, 200000
	tests := []struct {
		name string
		html func(controls string) string
	}{
		{"wrapping label", func(c string) string {
			return `<form><label>` + strings.Repeat("é", labelRunes) + c + `</label></form>`
		}},
		{"aria-labelledby", func(c string) string {
			return `<form><p id=big>` + strings.Repeat("é", labelRunes) + `</p>` + strings.ReplaceAll(c, "<input", "<input aria-labelledby=big") + `</form>`
		}},
		{"legend", func(c string) string {
			return `<form><fieldset><legend>` + strings.Repeat("é", labelRunes) + `</legend>` + strings.ReplaceAll(c, "<input", "<input type=checkbox") + `</fieldset></form>`
		}},
	}
	var controls strings.Builder
	for i := range inputs {
		controls.WriteString(`<input name=f` + strconv.Itoa(i) + `>`)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := tt.html(controls.String())
			var f Form
			bytes := allocated(func() { f = onlyForm(t, src) })
			t.Logf("page %d bytes, parse allocated %d bytes", len(src), bytes)
			if len(f.Fields) != inputs {
				t.Fatalf("got %d fields, want %d", len(f.Fields), inputs)
			}
			total := 0
			for _, fd := range f.Fields {
				if n := utf8.RuneCountInString(fd.Label); n != maxLabelRunes {
					t.Fatalf("label of %s is %d runes, want %d", fd.Name, n, maxLabelRunes)
				}
				total += len(fd.Label)
			}
			if limit := inputs * maxLabelRunes * utf8.UTFMax; total > limit {
				t.Errorf("labels take %d bytes, want at most %d", total, limit)
			}
			if bytes > labelCostAllocBound {
				t.Errorf("parse allocated %d bytes, want at most %d: a shared label's text must be computed once", bytes, labelCostAllocBound)
			}
		})
	}
}

func TestParseLabelledByManyRefs(t *testing.T) {
	const refs = 100000
	src := `<form><p id=x>` + strings.Repeat("é", 300) + `</p><input name=a aria-labelledby="` + strings.Repeat("x ", refs) + `"></form>`
	var f Form
	bytes := allocated(func() { f = onlyForm(t, src) })
	t.Logf("page %d bytes, parse allocated %d bytes", len(src), bytes)
	if n := utf8.RuneCountInString(f.Fields[0].Label); n != maxLabelRunes {
		t.Errorf("label is %d runes, want %d", n, maxLabelRunes)
	}
	if bytes > labelledByAllocBound {
		t.Errorf("parse allocated %d bytes, want at most %d: aria-labelledby must stop at the label limit", bytes, labelledByAllocBound)
	}
}

func TestParseDisabled(t *testing.T) {
	f := onlyForm(t, `<form>
<input name=own disabled>
<fieldset disabled><legend>L <input name=inlegend></legend>
<legend><input name=secondlegend></legend>
<div><input name=deep><select name=sel><option>a</option></select></div>
</fieldset>
<fieldset><input name=open></fieldset>
</form>`)
	want := map[string]bool{"own": true, "inlegend": false, "secondlegend": true, "deep": true, "sel": true, "open": false}
	got := map[string]bool{}
	for _, fd := range f.Fields {
		got[fd.Name] = fd.Disabled
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("disabled (-want +got):\n%s", diff)
	}
}

func TestParseFormOwnership(t *testing.T) {
	p := mustParse(t, `
<input name=before form=second>
<form id=first><input name=a><input name=moved form=second><input name=lost form=nowhere></form>
<form id=second><input name=b></form>
<input name=after form=second>
<input name=orphan>
<div id=second-not-a-form></div><input name=wrong form=second-not-a-form>`)
	got := map[string][]string{}
	for _, f := range p.Forms {
		for _, fd := range f.Fields {
			got[f.ID] = append(got[f.ID], fd.Name)
		}
	}
	want := map[string][]string{"first": {"a"}, "second": {"before", "moved", "b", "after"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fields by form (-want +got):\n%s", diff)
	}
}

func TestParseFormInTable(t *testing.T) {
	p := mustParse(t, `<table><form action="/t" method=post><tr><td><input name=a></td></tr>
<tr><td><select name=s><option>x</option></select></td></tr></form></table><input name=outside>`)
	if len(p.Forms) != 1 {
		t.Fatalf("got %d forms", len(p.Forms))
	}
	var names []string
	for _, fd := range p.Forms[0].Fields {
		names = append(names, fd.Name)
	}
	if diff := cmp.Diff([]string{"a", "s"}, names); diff != "" {
		t.Errorf("fields (-want +got):\n%s", diff)
	}
}

// Every emptied form of a table owns the controls up to the next one, and
// finding the owners is not quadratic in forms × controls.
func TestParseManyFormsInTable(t *testing.T) {
	const n = 4000
	page := func(n int) string {
		var b strings.Builder
		b.WriteString(`<table>`)
		for i := range n {
			b.WriteString(`<form id=f` + strconv.Itoa(i) + `></form><tr><td><input name=c` + strconv.Itoa(i) + `></td></tr>`)
		}
		b.WriteString(`</table>`)
		return b.String()
	}
	p := mustParse(t, page(n))
	if len(p.Forms) != n {
		t.Fatalf("got %d forms, want %d", len(p.Forms), n)
	}
	for i, f := range p.Forms {
		if len(f.Fields) != 1 || f.Fields[0].Name != "c"+strconv.Itoa(i) || f.ID != "f"+strconv.Itoa(i) {
			t.Fatalf("form %d (%s) owns %v", i, f.ID, f.Fields)
		}
	}
	small, large := page(n), page(4*n)
	growth := growthOf(func() { mustParse(t, small) }, func() { mustParse(t, large) })
	t.Logf("parsing 4× the forms took %.1f× as long", growth)
	if growth > 10 {
		t.Errorf("parsing 4× the forms took %.1f× as long; the reader must stay linear (about 4×, a forms × controls scan about 16×)", growth)
	}
}

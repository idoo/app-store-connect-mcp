package webpage

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const fillForm = `<form action="/save" method=post>
<input type=hidden name=csrf value="tok en">
<input name=title value="Old title" maxlength=10 required>
<textarea name=notes>a
b</textarea>
<input type=radio name=kind value=app checked><input type=radio name=kind value=game><input type=radio name=kind value=old disabled>
<input type=checkbox name=opt value=x checked><input type=checkbox name=opt value=y><input type=checkbox name=opt value=z disabled>
<input type=checkbox name=terms required>
<select name=team><option value=t1>One</option><option value=t2 selected>Two</option><option value=t3 disabled>Three</option></select>
<select name=langs multiple><option>en</option><option selected>sv</option><option>ru</option></select>
<input name=frozen value=ice disabled>
<input type=submit name=action value=save><input type=submit name=action value=delete>
<button name=go value=1>Go</button><button name=dead disabled>Dead</button>
</form>`

func pairs(fd Filled) []Pair { return fd.Pairs }

func TestFillDefaultsAndOverrides(t *testing.T) {
	f := onlyForm(t, fillForm)
	ticked := map[string][]string{"terms": {"on"}}
	with := func(extra map[string][]string) map[string][]string {
		m := map[string][]string{"terms": {"on"}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	base := []Pair{
		{Name: "csrf", Value: "tok en", Hidden: true},
		{Name: "title", Value: "Old title"},
		{Name: "notes", Value: "a\r\nb"},
		{Name: "kind", Value: "app"},
		{Name: "opt", Value: "x"},
		{Name: "terms", Value: "on"},
		{Name: "team", Value: "t2"},
		{Name: "langs", Value: "sv"},
	}
	replace := func(name string, with ...Pair) []Pair {
		var out []Pair
		for _, p := range base {
			if p.Name == name {
				out = append(out, with...)
				with = nil
				continue
			}
			out = append(out, p)
		}
		return out
	}
	tests := []struct {
		name string
		a    Answers
		want []Pair
	}{
		{"defaults", Answers{Values: ticked}, base},
		{"text", Answers{Values: with(map[string][]string{"title": {"New"}})}, replace("title", Pair{Name: "title", Value: "New"})},
		{"text loses line breaks", Answers{Values: with(map[string][]string{"title": {"A\r\nB"}})}, replace("title", Pair{Name: "title", Value: "AB"})},
		{"hidden", Answers{Values: with(map[string][]string{"csrf": {"other"}})}, replace("csrf", Pair{Name: "csrf", Value: "other", Answered: true})},
		{"textarea crlf", Answers{Values: with(map[string][]string{"notes": {"x\ny\rz\r\nw"}})}, replace("notes", Pair{Name: "notes", Value: "x\r\ny\r\nz\r\nw"})},
		{"radio", Answers{Values: with(map[string][]string{"kind": {"game"}})}, replace("kind", Pair{Name: "kind", Value: "game"})},
		{"radio cleared", Answers{Values: with(map[string][]string{"kind": {}})}, replace("kind")},
		{
			"checkboxes in option order",
			Answers{Values: with(map[string][]string{"opt": {"y", "x"}})},
			replace("opt", Pair{Name: "opt", Value: "x"}, Pair{Name: "opt", Value: "y"}),
		},
		{"checkboxes cleared", Answers{Values: with(map[string][]string{"opt": {}})}, replace("opt")},
		{"select", Answers{Values: with(map[string][]string{"team": {"t1"}})}, replace("team", Pair{Name: "team", Value: "t1"})},
		{
			"multiple select",
			Answers{Values: with(map[string][]string{"langs": {"ru", "en"}})},
			replace("langs", Pair{Name: "langs", Value: "en"}, Pair{Name: "langs", Value: "ru"}),
		},
		{"submitter", Answers{Values: ticked, Submitter: "go"}, append(append([]Pair{}, base...), Pair{Name: "go", Value: "1"})},
		{"submitter by value", Answers{Values: ticked, Submitter: "action=delete"}, append(append([]Pair{}, base...), Pair{Name: "action", Value: "delete"})},
		{"emoji fits maxlength", Answers{Values: with(map[string][]string{"title": {"😀😀😀😀😀"}})}, replace("title", Pair{Name: "title", Value: "😀😀😀😀😀"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, problems := f.Fill(tt.a)
			if problems != nil {
				t.Fatalf("problems: %v", problems)
			}
			if diff := cmp.Diff(tt.want, pairs(got)); diff != "" {
				t.Errorf("Pairs (-want +got):\n%s", diff)
			}
			if got.ContentType != enctypeURLEncoded {
				t.Errorf("ContentType = %q", got.ContentType)
			}
		})
	}
}

func TestFillSecretPairs(t *testing.T) {
	const src = `<form method=post action=/x>
<input type=hidden name=token value=page-token>
<input type=password name=pw value=page-secret>
<input type=password name=empty>
<input name=plain value=p>
</form>`
	tests := []struct {
		name     string
		values   map[string][]string
		want     []Pair
		wantBody string
	}{
		{
			"hidden and prefilled password unanswered",
			nil,
			[]Pair{
				{Name: "token", Value: "page-token", Hidden: true},
				{Name: "pw", Value: "page-secret", Hidden: true},
				{Name: "empty", Value: ""},
				{Name: "plain", Value: "p"},
			},
			"token=page-token&pw=page-secret&empty=&plain=p",
		},
		{
			"hidden answered",
			map[string][]string{"token": {"mine"}},
			[]Pair{
				{Name: "token", Value: "mine", Answered: true},
				{Name: "pw", Value: "page-secret", Hidden: true},
				{Name: "empty", Value: ""},
				{Name: "plain", Value: "p"},
			},
			"token=mine&pw=page-secret&empty=&plain=p",
		},
		{
			"hidden answered with the page's value",
			map[string][]string{"token": {"page-token"}},
			[]Pair{
				{Name: "token", Value: "page-token", Answered: true},
				{Name: "pw", Value: "page-secret", Hidden: true},
				{Name: "empty", Value: ""},
				{Name: "plain", Value: "p"},
			},
			"token=page-token&pw=page-secret&empty=&plain=p",
		},
		{
			"password answered",
			map[string][]string{"pw": {"typed"}, "empty": {"also"}},
			[]Pair{
				{Name: "token", Value: "page-token", Hidden: true},
				{Name: "pw", Value: "typed"},
				{Name: "empty", Value: "also"},
				{Name: "plain", Value: "p"},
			},
			"token=page-token&pw=typed&empty=also&plain=p",
		},
	}
	f := onlyForm(t, src)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, problems := f.Fill(Answers{Values: tt.values})
			if problems != nil {
				t.Fatalf("problems: %v", problems)
			}
			if diff := cmp.Diff(tt.want, got.Pairs); diff != "" {
				t.Errorf("Pairs (-want +got):\n%s", diff)
			}
			if string(got.Body) != tt.wantBody {
				t.Errorf("Body = %q, want %q", got.Body, tt.wantBody)
			}
		})
	}
}

// A hidden control beside a visible one of the same name keeps the page's
// value unseen; the answer goes to the visible control.
func TestFillSecretBesideVisible(t *testing.T) {
	f := onlyForm(t, `<form><input type=hidden name=q value=page><input name=q value=old></form>`)
	got, problems := f.Fill(Answers{Values: map[string][]string{"q": {"new"}}})
	if problems != nil {
		t.Fatalf("problems: %v", problems)
	}
	want := []Pair{{Name: "q", Value: "page", Hidden: true}, {Name: "q", Value: "new"}}
	if diff := cmp.Diff(want, got.Pairs); diff != "" {
		t.Errorf("Pairs (-want +got):\n%s", diff)
	}
}

func TestFillReadonly(t *testing.T) {
	f := onlyForm(t, `<form method=post>
<input name=who value="Page Name" readonly required>
<input name=empty readonly required>
<input name=row value=fixed readonly><input name=row value=open>
<textarea name=note readonly>kept</textarea>
</form>`)
	t.Run("unanswered sends the page's values", func(t *testing.T) {
		got, problems := f.Fill(Answers{})
		if problems != nil {
			t.Fatalf("problems: %v", problems)
		}
		want := []Pair{{Name: "who", Value: "Page Name"}, {Name: "empty"}, {Name: "row", Value: "fixed"}, {Name: "row", Value: "open"}, {Name: "note", Value: "kept"}}
		if diff := cmp.Diff(want, got.Pairs); diff != "" {
			t.Errorf("Pairs (-want +got):\n%s", diff)
		}
		if string(got.Body) != "who=Page+Name&empty=&row=fixed&row=open&note=kept" {
			t.Errorf("Body = %q", got.Body)
		}
	})
	t.Run("an answer to a readonly field is refused", func(t *testing.T) {
		for _, name := range []string{"who", "note"} {
			got, problems := f.Fill(Answers{Values: map[string][]string{name: {"Other"}}})
			want := []Problem{{Field: name, Message: "readonly: the page sets this value"}}
			if diff := cmp.Diff(want, problems); diff != "" {
				t.Errorf("%s problems (-want +got):\n%s", name, diff)
			}
			if !cmp.Equal(got, Filled{}) {
				t.Errorf("%s: Filled = %+v, want zero", name, got)
			}
		}
	})
	t.Run("a shared name goes to the writable field", func(t *testing.T) {
		got, problems := f.Fill(Answers{Values: map[string][]string{"row": {"mine"}}})
		if problems != nil {
			t.Fatalf("problems: %v", problems)
		}
		if !bytes.Contains(got.Body, []byte("row=fixed&row=mine")) {
			t.Errorf("Body = %q", got.Body)
		}
	})
}

func TestSecretKind(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{KindHidden, true},
		{"password", true},
		{"text", false},
		{KindTextarea, false},
		{KindSubmit, false},
		{KindFile, false},
		{"", false},
	}
	for _, tt := range tests {
		if got := SecretKind(tt.kind); got != tt.want {
			t.Errorf("SecretKind(%q) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestFillProblems(t *testing.T) {
	f := onlyForm(t, fillForm)
	files := onlyForm(t, `<form method=post><input type=file name=doc required><input type=file name=off disabled><input name=t></form>`)
	tests := []struct {
		name string
		form Form
		a    Answers
		want []string // fields with problems, in order
	}{
		{"unknown name", f, Answers{Values: map[string][]string{"terms": {"on"}, "nope": {"x"}}}, []string{"nope"}},
		{"unknown file", files, Answers{Files: map[string]File{"nope": {Name: "a"}, "doc": {Name: "d"}}}, []string{"nope"}},
		{"radio value not offered", f, Answers{Values: map[string][]string{"terms": {"on"}, "kind": {"other"}}}, []string{"kind"}},
		{"radio value disabled", f, Answers{Values: map[string][]string{"terms": {"on"}, "kind": {"old"}}}, []string{"kind"}},
		{"checkbox value not offered", f, Answers{Values: map[string][]string{"terms": {"yes"}}}, []string{"terms", "terms"}},
		{"checkbox value disabled", f, Answers{Values: map[string][]string{"terms": {"on"}, "opt": {"z"}}}, []string{"opt"}},
		{"select value not offered", f, Answers{Values: map[string][]string{"terms": {"on"}, "team": {"t3"}}}, []string{"team"}},
		{"two radios", f, Answers{Values: map[string][]string{"terms": {"on"}, "kind": {"app", "game"}}}, []string{"kind"}},
		{"two for a single select", f, Answers{Values: map[string][]string{"terms": {"on"}, "team": {"t1", "t2"}}}, []string{"team"}},
		{"two for a text", f, Answers{Values: map[string][]string{"terms": {"on"}, "title": {"a", "b"}}}, []string{"title"}},
		{"required text empty", f, Answers{Values: map[string][]string{"terms": {"on"}, "title": {""}}}, []string{"title"}},
		{"required checkbox unticked", f, Answers{}, []string{"terms"}},
		{"required file missing", files, Answers{}, []string{"doc"}},
		{"maxlength", f, Answers{Values: map[string][]string{"terms": {"on"}, "title": {"12345678901"}}}, []string{"title"}},
		{"maxlength counts utf-16", f, Answers{Values: map[string][]string{"terms": {"on"}, "title": {"😀😀😀😀😀x"}}}, []string{"title"}},
		{"file for a text field", files, Answers{Files: map[string]File{"doc": {Name: "d"}, "t": {Name: "x"}}}, []string{"t"}},
		{"value for a file field", files, Answers{Values: map[string][]string{"doc": {"x"}}}, []string{"doc", "doc"}},
		{"disabled field", f, Answers{Values: map[string][]string{"terms": {"on"}, "frozen": {"x"}}}, []string{"frozen"}},
		{"disabled file field", files, Answers{Files: map[string]File{"doc": {Name: "d"}, "off": {Name: "x"}}}, []string{"off"}},
		{"unknown submitter", f, Answers{Values: map[string][]string{"terms": {"on"}}, Submitter: "nope"}, []string{"nope"}},
		{"ambiguous submitter", f, Answers{Values: map[string][]string{"terms": {"on"}}, Submitter: "action"}, []string{"action"}},
		{"disabled submitter", f, Answers{Values: map[string][]string{"terms": {"on"}}, Submitter: "dead"}, []string{"dead"}},
		{"value for a submit", f, Answers{Values: map[string][]string{"terms": {"on"}, "go": {"1"}}}, []string{"go"}},
		{
			"all reported", f,
			Answers{Values: map[string][]string{"nope": {"x"}, "kind": {"bad"}, "title": {""}}, Submitter: "zz"},
			[]string{"nope", "zz", "title", "kind", "terms"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, problems := tt.form.Fill(tt.a)
			var fields []string
			for _, p := range problems {
				if p.Message == "" {
					t.Errorf("problem for %q has no message", p.Field)
				}
				fields = append(fields, p.Field)
			}
			if diff := cmp.Diff(tt.want, fields); diff != "" {
				t.Errorf("problem fields (-want +got):\n%s\n%v", diff, problems)
			}
			if !cmp.Equal(got, Filled{}) {
				t.Errorf("Filled = %+v, want zero", got)
			}
		})
	}
}

func TestFillURLEncodedBytes(t *testing.T) {
	f := onlyForm(t, `<form method=post action=/x>
<input name="q" value="a b&c=d">
<input type=hidden name="ü key" value="é~*-._!'()">
<textarea name=t>1
2</textarea>
<input type=checkbox name=c value=on checked>
<input type=submit name=s value="Go now">
</form>`)
	got, problems := f.Fill(Answers{Submitter: "s"})
	if problems != nil {
		t.Fatal(problems)
	}
	want := "q=a+b%26c%3Dd&%C3%BC+key=%C3%A9%7E*-._%21%27%28%29&t=1%0D%0A2&c=on&s=Go+now"
	if string(got.Body) != want {
		t.Errorf("Body = %s\nwant   %s", got.Body, want)
	}
	if got.ContentType != "application/x-www-form-urlencoded" {
		t.Errorf("ContentType = %q", got.ContentType)
	}
}

func TestFillMultipart(t *testing.T) {
	f := onlyForm(t, `<form method=post action=/up>
<input type=hidden name=csrf value=t>
<input type=file name=cert>
<input name='say "hi"' value=x>
<textarea name=notes>a
b</textarea>
<input type=file name=empty>
<input type=checkbox name=c value=1 checked>
</form>`)
	data := []byte{0, 1, 2, '\r', '\n', 0xff}
	got, problems := f.Fill(Answers{
		Files:  map[string]File{"cert": {Name: "my\"cert\n.cer", MIME: "application/pkix-cert", Data: data}},
		Values: map[string][]string{"notes": {"one\ntwo"}},
	})
	if problems != nil {
		t.Fatal(problems)
	}
	wantPairs := []Pair{
		{Name: "csrf", Value: "t", Hidden: true},
		{Name: "cert", Value: "my\"cert\n.cer", File: true},
		{Name: `say "hi"`, Value: "x"},
		{Name: "notes", Value: "one\r\ntwo"},
		{Name: "empty", File: true},
		{Name: "c", Value: "1"},
	}
	if diff := cmp.Diff(wantPairs, got.Pairs); diff != "" {
		t.Errorf("Pairs (-want +got):\n%s", diff)
	}

	mt, params, err := mime.ParseMediaType(got.ContentType)
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		t.Fatalf("ContentType = %q (%v)", got.ContentType, err)
	}
	type part struct {
		Name, FileName, MIME, Data string
		IsFile                     bool
	}
	r := multipart.NewReader(bytes.NewReader(got.Body), params["boundary"])
	var parts []part
	for {
		p, err := r.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		_, dp, _ := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
		_, isFile := dp["filename"]
		parts = append(parts, part{Name: dp["name"], FileName: dp["filename"], MIME: p.Header.Get("Content-Type"), Data: string(b), IsFile: isFile})
	}
	wantParts := []part{
		{Name: "csrf", Data: "t"},
		{Name: "cert", FileName: "my%22cert%0A.cer", MIME: "application/pkix-cert", Data: string(data), IsFile: true},
		{Name: "say %22hi%22", Data: "x"},
		{Name: "notes", Data: "one\r\ntwo"},
		{Name: "empty", MIME: "application/octet-stream", IsFile: true},
		{Name: "c", Data: "1"},
	}
	if diff := cmp.Diff(wantParts, parts); diff != "" {
		t.Errorf("parts (-want +got):\n%s", diff)
	}

	again, _ := f.Fill(Answers{Files: map[string]File{"cert": {Name: "c"}}})
	if again.ContentType == got.ContentType {
		t.Error("boundary repeats between fills")
	}
	if !strings.Contains(string(again.Body), "Content-Type: application/octet-stream\r\n\r\n") {
		t.Error("file without MIME is not application/octet-stream")
	}
}

func TestFillFileInURLEncodedForm(t *testing.T) {
	f := Form{Action: "https://x/", Method: "POST", Enctype: enctypeURLEncoded, Fields: []Field{{Name: "f", Kind: KindFile}, {Name: "t", Kind: "text", Values: []string{"v"}}}}
	got, problems := f.Fill(Answers{})
	if problems != nil {
		t.Fatal(problems)
	}
	if string(got.Body) != "t=v" {
		t.Errorf("Body = %q, want an absent file left out", got.Body)
	}
	if _, problems := f.Fill(Answers{Files: map[string]File{"f": {Name: "a"}}}); len(problems) != 1 {
		t.Errorf("problems = %v, want one for a file the form can't send", problems)
	}
}

func TestFillSharedNames(t *testing.T) {
	f := onlyForm(t, `<form>
<input type=hidden name=flag value=0><input type=checkbox name=flag value=1>
<input name=row><input name=row><input name=row>
</form>`)
	got, problems := f.Fill(Answers{Values: map[string][]string{"flag": {"1"}, "row": {"a", "b", "c"}}})
	if problems != nil {
		t.Fatal(problems)
	}
	if string(got.Body) != "flag=0&flag=1&row=a&row=b&row=c" {
		t.Errorf("Body = %s", got.Body)
	}
	if _, problems := f.Fill(Answers{Values: map[string][]string{"row": {"a", "b"}}}); len(problems) != 1 || problems[0].Field != "row" {
		t.Errorf("problems = %v, want one for row", problems)
	}
}

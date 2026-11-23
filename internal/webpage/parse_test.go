package webpage

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const pageURL = "https://developer.apple.com/account/resources/page?x=1#top"

func mustParse(t *testing.T, src string) Page {
	t.Helper()
	p, err := Parse(strings.NewReader(src), pageURL)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func TestParseText(t *testing.T) {
	tests := []struct {
		name, html, want string
	}{
		{"blocks", `<div>one</div><div>two</div>`, "one\ntwo"},
		{"paragraphs stand apart", `<p>one</p><p>two</p>`, "one\n\ntwo"},
		{"white space collapses", "<p>  a \n\t b\u00a0 c  </p>", "a b c"},
		{"no repeated blank lines", `<p>a</p><p></p><p> </p><hr><p>b</p>`, "a\n\nb"},
		{
			"headings", `<h1>One</h1><h2>Two</h2><h3>Three</h3><h5>Five</h5><p>body</p>`,
			"# One\n\n## Two\n\n### Three\n\n### Five\n\nbody",
		},
		{"lists", `<ul><li>a</li><li>b <b>bold</b></li></ul><ol><li>c</li></ol>`, "- a\n- b bold\n\n- c"},
		{"br", `<p>a<br>b<br/>c</p>`, "a\nb\nc"},
		{"double br leaves a blank line", `<div>a<br><br>b</div>`, "a\n\nb"},
		{"hidden parts", `<head><title>T</title><style>p{}</style></head><body>
			<script>var x=1</script><noscript>enable js</noscript><template><p>tpl</p></template>
			<svg><text>vector</text></svg><p hidden>gone</p><div aria-hidden="true">aria</div>
			<div aria-hidden="false">shown</div></body>`, "shown"},
		{"entities", `<p>a &amp; b &lt;c&gt; &eacute;&nbsp;d &#x1F600;</p>`, "a & b <c> é d 😀"},
		{"nested inline", `<p>a<b>b<i>c</i></b> <span>d</span>e</p>`, "abc de"},
		{"img alt", `<p>see <img src=x alt=" a  chart "> and <img src=y></p>`, "see [a chart] and"},
		{"control text left out, label kept", `<form><label>Name <input name=n value=v></label>
			<textarea name=t>typed</textarea><select name=s><option>opt</option></select>
			<button>Go</button></form>`, "Name"},
		{"table cells", `<table><tr><td>a</td><td>b</td></tr><tr><th>c</th></tr></table>`, "a b\nc"},
		{"heading prefix only with text", `<h2><img src=x></h2><p>p</p>`, "p"},
		{"inline across blocks", `x<div>y</div>z`, "x\ny\nz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustParse(t, tt.html).Text; got != tt.want {
				t.Errorf("Text = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseTitle(t *testing.T) {
	tests := []struct{ name, html, want string }{
		{"title", `<title> Certificates,  IDs </title><h1>Head</h1>`, "Certificates, IDs"},
		{"first h1", `<title> </title><h1 hidden>no</h1><h1>Account <small>x</small></h1><h1>two</h1>`, "Account x"},
		{"none", `<p>body</p>`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustParse(t, tt.html).Title; got != tt.want {
				t.Errorf("Title = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseLinks(t *testing.T) {
	tests := []struct {
		name, html string
		want       []Link
	}{
		{
			"relative to page", `<a href="other">Other</a><a href="/root?q=1">Root</a><a href="https://example.com/x">Ext</a>`,
			[]Link{
				{"Other", "https://developer.apple.com/account/resources/other"},
				{"Root", "https://developer.apple.com/root?q=1"},
				{"Ext", "https://example.com/x"},
			},
		},
		{
			"base href", `<head><base href="/services-account/"><base href="/ignored/"></head><a href="v1/x">X</a>`,
			[]Link{{"X", "https://developer.apple.com/services-account/v1/x"}},
		},
		{
			"duplicates and same href other text", `<a href="/a">A</a><a href="/a"> A </a><a href="/a">Again</a>`,
			[]Link{{"A", "https://developer.apple.com/a"}, {"Again", "https://developer.apple.com/a"}},
		},
		{"left out", `<a href="mailto:x@y.z">Mail</a><a href="javascript:void(0)">JS</a><a href="#top">Top</a>
			<a href="">Empty</a><a href="/x"></a><a href="/y"><img src=i></a><a>No href</a><p hidden><a href="/h">H</a></p>
			<a href="ftp://h/f">FTP</a>`, nil},
		{"img alt is text", `<a href="/z"><img src=i alt="Zed"></a>`, []Link{{"[Zed]", "https://developer.apple.com/z"}}},
		{"fragment on other page kept", `<a href="/p#sec">P</a>`, []Link{{"P", "https://developer.apple.com/p#sec"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, mustParse(t, tt.html).Links); diff != "" {
				t.Errorf("Links (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseScripted(t *testing.T) {
	long := strings.Repeat("word ", 50)
	tests := []struct {
		name, html string
		want       bool
	}{
		{"app shell", `<div id=root></div><script src="/app.js"></script>`, true},
		{"inline script only", `<div id=root></div><script>boot()</script>`, false},
		{"text and script", `<p>` + long + `</p><script src="/app.js"></script>`, false},
		{"short page, no script", `<p>hi</p>`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustParse(t, tt.html).Scripted; got != tt.want {
				t.Errorf("Scripted = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseNotHTML(t *testing.T) {
	for _, src := range []string{``, `{"resultCode":0,"teams":[]}`, "plain text\nbody", `<!-- only a comment -->`, `<!DOCTYPE html>`} {
		if _, err := Parse(strings.NewReader(src), pageURL); !errors.Is(err, ErrNotHTML) {
			t.Errorf("Parse(%q) error = %v, want ErrNotHTML", src, err)
		}
	}
	if _, err := Parse(strings.NewReader(`<p>x</p>`), pageURL); err != nil {
		t.Errorf("Parse of a fragment: %v", err)
	}
}

// FuzzParse holds what must be true of any input, however broken (a page
// is third-party data): links are absolute http(s) URLs, and a form either
// fills into a body of its own enctype or says why not.
func FuzzParse(f *testing.F) {
	rich := `<!DOCTYPE html><html><head><base href="/b/"><title>T</title><script src=a.js></script></head>
<body><h1>H</h1><p>text &amp; <a href="x">link</a></p><table><form id=f action=go method=post>
<tr><td><input name=a value=1><select name=s><optgroup label=g disabled><option>o</option></optgroup></select>
</td></tr></table><fieldset disabled><legend><input name=l></legend><input type=radio name=r value=1 checked>
<input type=checkbox name=c><input type=file name=up></fieldset><textarea name=t>
x</textarea><button name=b>Go</button><input form=f name=out><ul><li>a<li>b</ul></body></html>`
	for _, src := range []string{"\x00\xff\xfe<", "<<<>>>", "<a href=", "<form><input type=radio", "<select><option", "&#xFFFFFFFF;", "<svg><foreignObject><p>x"} {
		f.Add(src)
	}
	for i := 0; i <= len(rich); i += 7 {
		f.Add(rich[:i])
	}
	f.Fuzz(func(t *testing.T, src string) {
		p, err := Parse(strings.NewReader(src), pageURL)
		if err != nil {
			return
		}
		for _, l := range p.Links {
			u, err := url.Parse(l.Href)
			if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
				t.Errorf("link %q is not an absolute http(s) URL", l.Href)
			}
		}
		for _, form := range p.Forms {
			filled, problems := form.Fill(Answers{Values: map[string][]string{"a": {"z"}}, Submitter: "b"})
			if len(problems) == 0 && !strings.HasPrefix(filled.ContentType, form.Enctype) {
				t.Errorf("form %q: filled as %q, its enctype is %q", form.ID, filled.ContentType, form.Enctype)
			}
		}
	})
}

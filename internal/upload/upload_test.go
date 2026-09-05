package upload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

func TestValidateOperations(t *testing.T) {
	ok := splitOps(100, 3)
	mod := func(f func(ops []Operation) []Operation) []Operation {
		return f(slices.Clone(ok))
	}
	cases := []struct {
		name string
		ops  []Operation
		want string
	}{
		{"ok", ok, ""},
		{"empty", nil, "no upload operations"},
		{"http", mod(func(o []Operation) []Operation { o[0].URL = "http://upload.example.com/x"; return o }), "https only"},
		{"port", mod(func(o []Operation) []Operation { o[0].URL = "https://upload.example.com:8443/x"; return o }), "443 only"},
		{"userinfo", mod(func(o []Operation) []Operation { o[0].URL = "https://u:p@upload.example.com/x"; return o }), "userinfo"},
		{"method", mod(func(o []Operation) []Operation { o[1].Method = "DELETE"; return o }), "PUT or POST"},
		{"beyond", mod(func(o []Operation) []Operation { o[2].Length++; return o }), "outside file"},
		{"gap", mod(func(o []Operation) []Operation { o[1].Offset++; o[1].Length--; return o }), "gap or overlap"},
		{"short", mod(func(o []Operation) []Operation { o[2].Length--; return o }), "cover 99 of 100"},
		{"negative", mod(func(o []Operation) []Operation { o[0].Offset = -1; return o }), "outside file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateOperations(c.ops, 100)
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			mustContain(t, err, c.want)
		})
	}
	var many []Operation
	for i := range MaxUploadHosts + 1 {
		many = append(many, Operation{Method: "PUT", URL: fmt.Sprintf("https://h%d.example.com/p", i), Offset: int64(i), Length: 1})
	}
	mustContain(t, ValidateOperations(many, int64(len(many))), "hosts")
}

func TestFileMD5(t *testing.T) {
	got, err := FileMD5(strings.NewReader("hello"))
	if err != nil || got != "5d41402abc4b2a76b9719d911017c592" {
		t.Fatalf("md5 = %q, %v", got, err)
	}
}

func TestPutPartsRetryHeadersAndBytes(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 1000)
	ops := splitOps(int64(len(data)), 5)
	f := newRecordingFetcher(t)
	for i := range ops {
		f.inner.Handle("PUT", partURL(i), http.StatusOK, nil)
	}
	f.failures[partURL(3)] = 2
	clock := tooltest.NewFakeClock()
	var last int64
	err := PutParts(context.Background(), f, clock, bytes.NewReader(data), int64(len(data)), ops, func(done, total int64) {
		if done < last || total != int64(len(data)) {
			t.Errorf("progress %d/%d after %d", done, total, last)
		}
		last = done
	})
	if err != nil {
		t.Fatal(err)
	}
	if last != int64(len(data)) {
		t.Errorf("final progress = %d", last)
	}
	if clock.Slept != 500*time.Millisecond+time.Second {
		t.Errorf("backoff slept %v", clock.Slept)
	}
	calls := f.snapshot()
	if len(calls) != len(ops)+2 {
		t.Fatalf("calls = %d", len(calls))
	}
	got := make([][]byte, len(ops))
	for _, c := range calls {
		if c.Header.Get("Authorization") != "" || c.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("%s headers = %v", c.URL, c.Header)
		}
		for i := range ops {
			if c.URL == partURL(i) {
				got[i] = c.Body
			}
		}
	}
	if !bytes.Equal(bytes.Join(got, nil), data) {
		t.Error("uploaded bytes differ from file")
	}
}

func TestPutPartsGivesUp(t *testing.T) {
	data := []byte("abcdef")
	ops := splitOps(6, 2)
	f := newRecordingFetcher(t)
	f.inner.Handle("PUT", partURL(0), http.StatusOK, nil)
	f.failures[partURL(1)] = 10
	err := PutParts(context.Background(), f, tooltest.NewFakeClock(), bytes.NewReader(data), 6, ops, nil)
	mustContain(t, err, "after 4 attempts")

	f = newRecordingFetcher(t)
	f.failStatus = http.StatusForbidden
	f.failures[partURL(0)] = 1
	f.inner.Handle("PUT", partURL(1), http.StatusOK, nil)
	err = PutParts(context.Background(), f, tooltest.NewFakeClock(), bytes.NewReader(data), 6, ops, nil)
	mustContain(t, err, "returned 403")
	if n := strings.Count(fmt.Sprint(f.snapshot()), partURL(0)); n != 1 {
		t.Errorf("403 retried: %d calls", n)
	}
}

// screenshotFixture registers the asset flow for one screenshot with the
// given poll states.
func screenshotFixture(t *testing.T, k *tooltest.Kit, size int64, states ...string) {
	ops := splitOps(size, 2)
	k.ASC.Handle("POST", "/v1/appScreenshots", http.StatusCreated, resourceJSON(t, "appScreenshots", "shot-1", map[string]any{
		"fileName": "01-home.png", "fileSize": size, "uploadOperations": ops,
		"assetDeliveryState": map[string]any{"state": "AWAITING_UPLOAD"},
	}))
	k.ASC.Handle("PATCH", "/v1/appScreenshots/{id}", http.StatusOK, resourceJSON(t, "appScreenshots", "shot-1", map[string]any{
		"assetDeliveryState": map[string]any{"state": "UPLOAD_COMPLETE"},
	}))
	for _, s := range states {
		st := map[string]any{"state": s}
		if s == StateFailed {
			st["errors"] = []map[string]string{{"code": "IMAGE_TOO_LARGE", "description": "The image dimensions are wrong."}}
		}
		k.ASC.Handle("GET", "/v1/appScreenshots/{id}", http.StatusOK, resourceJSON(t, "appScreenshots", "shot-1", map[string]any{"assetDeliveryState": st}))
	}
	for i := range ops {
		k.Fetch.Handle("PUT", partURL(i), http.StatusOK, nil)
	}
}

func screenshotSpec(data []byte) AssetSpec {
	return AssetSpec{
		CreatePath: "/v1/appScreenshots", Type: "appScreenshots",
		Parent:   Relationship{Name: "appScreenshotSet", Type: "appScreenshotSets", ID: "set-1"},
		FileName: "01-home.png", FileSize: int64(len(data)), File: bytes.NewReader(data),
	}
}

func TestUploadAssetFlow(t *testing.T) {
	k := tooltest.New(t)
	data := bytes.Repeat([]byte{0xAB}, 4096)
	screenshotFixture(t, k, int64(len(data)), StateUploadComplete, StateComplete)
	res, err := Asset(writeCtx(t), DepsFromEnv(k.Env), screenshotSpec(data))
	if err != nil {
		t.Fatal(err)
	}
	if res.State != StateComplete || res.StillProcessing || res.ID != "shot-1" {
		t.Fatalf("result = %+v", res)
	}
	want, _ := FileMD5(bytes.NewReader(data))
	if res.Checksum != want {
		t.Errorf("checksum = %s, want %s", res.Checksum, want)
	}
	if !slices.Equal(k.Fetch.Calls, []string{"PUT " + partURL(0), "PUT " + partURL(1)}) &&
		!slices.Equal(k.Fetch.Calls, []string{"PUT " + partURL(1), "PUT " + partURL(0)}) {
		t.Errorf("fetch calls = %v", k.Fetch.Calls)
	}
	tooltest.AssertGolden(t, "testdata/golden/asset_flow.json", map[string]any{
		"requests": requestLog(k.ASC.Requests()), "result": res,
	})
}

func TestUploadAssetPreviewAttributes(t *testing.T) {
	k := tooltest.New(t)
	data := []byte("moov-video-bytes")
	ops := splitOps(int64(len(data)), 1)
	k.ASC.Handle("POST", "/v1/appPreviews", http.StatusCreated, resourceJSON(t, "appPreviews", "pv-1", map[string]any{"uploadOperations": ops}))
	k.ASC.Handle("PATCH", "/v1/appPreviews/{id}", http.StatusOK, resourceJSON(t, "appPreviews", "pv-1", map[string]any{
		"assetDeliveryState": map[string]any{"state": "COMPLETE"},
	}))
	k.Fetch.Handle("PUT", partURL(0), http.StatusOK, nil)
	res, err := Asset(writeCtx(t), DepsFromEnv(k.Env), AssetSpec{
		CreatePath: "/v1/appPreviews", Type: "appPreviews",
		Parent:   Relationship{Name: "appPreviewSet", Type: "appPreviewSets", ID: "ps-1"},
		FileName: "preview.mov", FileSize: int64(len(data)), File: bytes.NewReader(data),
		Attributes:       map[string]any{"mimeType": "video/quicktime"},
		CommitAttributes: map[string]any{"previewFrameTimeCode": "00:00:05:00"},
	})
	if err != nil || res.State != StateComplete {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	muts := k.ASC.Mutations()
	reserve := tooltest.JSON(t, muts[0].Body)
	commit := tooltest.JSON(t, muts[1].Body)
	if !strings.Contains(reserve, `"mimeType":"video/quicktime"`) || !strings.Contains(commit, `"previewFrameTimeCode":"00:00:05:00"`) {
		t.Errorf("reserve=%s commit=%s", reserve, commit)
	}
	if len(k.ASC.RequestsTo("GET", "/v1/appPreviews/{id}")) != 0 {
		t.Error("polled although commit returned COMPLETE")
	}
}

func TestUploadAssetStateField(t *testing.T) {
	k := tooltest.New(t)
	data := []byte("moov-video-bytes")
	ops := splitOps(int64(len(data)), 1)
	k.ASC.Handle("POST", "/v1/appPreviews", http.StatusCreated, resourceJSON(t, "appPreviews", "pv-1", map[string]any{"uploadOperations": ops}))
	// The deprecated assetDeliveryState reports COMPLETE right after commit
	// while the video is still being transcoded.
	k.ASC.Handle("PATCH", "/v1/appPreviews/{id}", http.StatusOK, resourceJSON(t, "appPreviews", "pv-1", map[string]any{
		"assetDeliveryState": map[string]any{"state": "COMPLETE"},
		"videoDeliveryState": map[string]any{"state": "UPLOAD_COMPLETE"},
	}))
	k.ASC.Handle("GET", "/v1/appPreviews/{id}", http.StatusOK, resourceJSON(t, "appPreviews", "pv-1", map[string]any{
		"assetDeliveryState": map[string]any{"state": "COMPLETE"},
		"videoDeliveryState": map[string]any{"state": "PROCESSING"},
	}))
	k.ASC.Handle("GET", "/v1/appPreviews/{id}", http.StatusOK, resourceJSON(t, "appPreviews", "pv-1", map[string]any{
		"assetDeliveryState": map[string]any{"state": "COMPLETE"},
		"videoDeliveryState": map[string]any{"state": "COMPLETE", "warnings": []map[string]string{{"code": "W", "description": "short"}}},
	}))
	k.Fetch.Handle("PUT", partURL(0), http.StatusOK, nil)
	res, err := Asset(writeCtx(t), DepsFromEnv(k.Env), AssetSpec{
		CreatePath: "/v1/appPreviews", Type: "appPreviews",
		Parent:   Relationship{Name: "appPreviewSet", Type: "appPreviewSets", ID: "ps-1"},
		FileName: "preview.mov", FileSize: int64(len(data)), File: bytes.NewReader(data),
		StateField: "videoDeliveryState",
	})
	if err != nil || res.State != StateComplete || len(res.Warnings) != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/appPreviews/{id}")); n != 2 {
		t.Errorf("polls = %d, want 2 (until videoDeliveryState COMPLETE)", n)
	}
	if (AssetSpec{}).stateField() != DefaultStateField || DefaultStateField != "assetDeliveryState" {
		t.Error("default state field")
	}
}

func TestUploadAssetFailedSurfacesErrors(t *testing.T) {
	k := tooltest.New(t)
	data := []byte("not really a png")
	screenshotFixture(t, k, int64(len(data)), StateFailed)
	res, err := Asset(writeCtx(t), DepsFromEnv(k.Env), screenshotSpec(data))
	if !errors.Is(err, ErrUploadFailed) {
		t.Fatalf("err = %v", err)
	}
	mustContain(t, err, "IMAGE_TOO_LARGE: The image dimensions are wrong.")
	if res == nil || res.State != StateFailed || len(res.Errors) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if body := tool.BodyFor(err); body.Error != "upload_failed" {
		t.Errorf("error body = %+v", body)
	}
}

func TestUploadAssetStillProcessing(t *testing.T) {
	k := tooltest.New(t)
	data := []byte("pixels")
	screenshotFixture(t, k, int64(len(data)), StateUploadComplete)
	spec := screenshotSpec(data)
	spec.Poll = PollOptions{Interval: 2 * time.Second, MaxWait: 9 * time.Second}
	res, err := Asset(writeCtx(t), DepsFromEnv(k.Env), spec)
	if err != nil || !res.StillProcessing || res.State != StateUploadComplete {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if k.Clock.Slept != 9*time.Second {
		t.Errorf("waited %v", k.Clock.Slept)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/appScreenshots/{id}")); n != 5 {
		t.Errorf("polls = %d", n)
	}
}

func TestUploadAssetNeedsWriteContext(t *testing.T) {
	k := tooltest.New(t)
	data := []byte("x")
	_, err := Asset(context.Background(), DepsFromEnv(k.Env), screenshotSpec(data))
	if !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestParsePlistVariants(t *testing.T) {
	bin := encodeBplist(t, map[string]any{
		"CFBundleShortVersionString": "2.3.1", "CFBundleVersion": "417",
		"CFBundleDisplayName": "Hjül", "Families": []any{int64(1), int64(2)}, "Phone": true,
		"LongKeyNameForLengthMarker": "x",
	})
	v, err := ParsePlist(bin)
	if err != nil {
		t.Fatal(err)
	}
	d := v.(map[string]any)
	if d["CFBundleDisplayName"] != "Hjül" || d["Phone"] != true || !slices.Equal(d["Families"].([]any), []any{int64(1), int64(2)}) || d["LongKeyNameForLengthMarker"] != "x" {
		t.Errorf("binary = %#v", d)
	}
	v, err = ParsePlist([]byte(xmlInfoPlist))
	if err != nil {
		t.Fatal(err)
	}
	d = v.(map[string]any)
	if d["CFBundleVersion"] != "417" || d["LSRequiresIPhoneOS"] != true || string(d["Blob"].([]byte)) != "hello" || d["Scale"] != 2.5 {
		t.Errorf("xml = %#v", d)
	}
	for _, bad := range [][]byte{
		[]byte("bplist00short"),
		append(bin[:len(bin)-8:len(bin)-8], 0xFF, 0, 0, 0, 0, 0, 0, 0),
		[]byte("<plist><dict><string>x</string></dict></plist>"),
		[]byte("<html/>"),
	} {
		if _, err := ParsePlist(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseBinaryPlistCycle(t *testing.T) {
	// Object 0 is an array whose single element refers to itself.
	b := []byte("bplist00\xA1\x00")
	b = append(b, 0x08) // offset table: object 0 at 8
	b = append(b, 0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10)
	mustContain(t, func() error { _, err := ParsePlist(b); return err }(), "cyclic")
}

func FuzzParsePlist(f *testing.F) {
	f.Add([]byte(xmlInfoPlist))
	f.Add([]byte("bplist00\xA1\x00\x08\x00\x00\x00\x00\x00\x00\x01\x01\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x0a"))
	f.Fuzz(func(_ *testing.T, b []byte) { _, _ = ParsePlist(b) })
}

func TestIPAVersions(t *testing.T) {
	for name, plist := range map[string][]byte{
		"binary": encodeBplist(t, map[string]any{"CFBundleShortVersionString": "2.3.1", "CFBundleVersion": "417"}),
		"xml":    []byte(xmlInfoPlist),
	} {
		t.Run(name, func(t *testing.T) {
			ipa := makeIPA(t, plist)
			v, b, err := IPAVersions(bytes.NewReader(ipa), int64(len(ipa)))
			if err != nil || v != "2.3.1" || b != "417" {
				t.Fatalf("got %q %q %v", v, b, err)
			}
		})
	}
	ipa := makeIPA(t, []byte(`<plist><dict><key>CFBundleVersion</key><string>1</string></dict></plist>`))
	_, _, err := IPAVersions(bytes.NewReader(ipa), int64(len(ipa)))
	mustContain(t, err, "lacks CFBundleShortVersionString")
}

// buildFixture registers the build flow for ipa (3 parts).
func buildFixture(t *testing.T, k *tooltest.Kit, size int64) []Operation {
	ops := splitOps(size, 3)
	k.ASC.Handle("GET", "/v1/apps/{id}/buildUploads", http.StatusOK, `{"data":[]}`)
	k.ASC.Handle("POST", "/v1/buildUploads", http.StatusCreated, resourceJSON(t, "buildUploads", "bu-1", map[string]any{
		"state": map[string]any{"state": "AWAITING_UPLOAD"},
	}))
	file := resourceJSON(t, "buildUploadFiles", "buf-1", map[string]any{
		"fileName": "Hjul.ipa", "fileSize": size, "uploadOperations": ops, "assetType": "ASSET", "uti": "com.apple.ipa",
	})
	k.ASC.Handle("POST", "/v1/buildUploadFiles", http.StatusCreated, file)
	k.ASC.Handle("GET", "/v1/buildUploadFiles/{id}", http.StatusOK, file)
	k.ASC.Handle("PATCH", "/v1/buildUploadFiles/{id}", http.StatusOK, resourceJSON(t, "buildUploadFiles", "buf-1", nil))
	k.ASC.Handle("GET", "/v1/buildUploads/{id}", http.StatusOK, resourceJSON(t, "buildUploads", "bu-1", map[string]any{
		"state": map[string]any{"state": "PROCESSING"},
	}))
	k.ASC.Handle("GET", "/v1/buildUploads/{id}", http.StatusOK, resourceJSON(t, "buildUploads", "bu-1", map[string]any{
		"state": map[string]any{"state": "COMPLETE", "warnings": []map[string]string{{"code": "W1", "description": "Missing icon size."}}},
	}))
	return ops
}

func ipaSpec(ipa []byte) BuildSpec {
	return BuildSpec{AppID: "app-1", FileName: "Hjul.ipa", File: bytes.NewReader(ipa), FileSize: int64(len(ipa))}
}

func TestUploadBuildFlow(t *testing.T) {
	k := tooltest.New(t)
	ipa := makeIPA(t, encodeBplist(t, map[string]any{"CFBundleShortVersionString": "2.3.1", "CFBundleVersion": "417"}))
	ops := buildFixture(t, k, int64(len(ipa)))
	for i := range ops {
		k.Fetch.Handle("PUT", partURL(i), http.StatusOK, nil)
	}
	res, err := Build(writeCtx(t), DepsFromEnv(k.Env), ipaSpec(ipa))
	if err != nil {
		t.Fatal(err)
	}
	if res.State != BuildStateComplete || res.Version != "2.3.1" || res.BuildNumber != "417" || res.Platform != tool.IOS || len(res.Warnings) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(k.Fetch.Calls) != 3 {
		t.Errorf("fetch calls = %v", k.Fetch.Calls)
	}
	_, key, err := fileDigests(bytes.NewReader(ipa))
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := k.State.Load(resumeKind, key, &resumeRecord{}); ok {
		t.Error("resume record kept after COMPLETE")
	}
	tooltest.AssertGolden(t, "testdata/golden/build_flow.json", map[string]any{
		"requests": requestLog(k.ASC.Requests()), "result": res,
	})
}

func TestUploadBuildResumeSkipsDoneParts(t *testing.T) {
	k := tooltest.New(t)
	ipa := makeIPA(t, []byte(xmlInfoPlist))
	ops := buildFixture(t, k, int64(len(ipa)))
	f := newRecordingFetcher(t)
	f.failStatus = http.StatusForbidden
	f.failures[partURL(2)] = 1
	for i := range ops {
		f.inner.Handle("PUT", partURL(i), http.StatusOK, nil)
	}
	d := DepsFromEnv(k.Env)
	d.Fetch = f
	ctx := writeCtx(t)
	_, err := Build(ctx, d, ipaSpec(ipa))
	mustContain(t, err, "call again with the same file to resume")
	if n := len(k.ASC.RequestsTo("PATCH", "/v1/buildUploadFiles/{id}")); n != 0 {
		t.Fatalf("committed after failure: %d", n)
	}

	first := len(f.snapshot())
	res, err := Build(ctx, d, ipaSpec(ipa))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Resumed || res.PartsSkipped != 2 || res.State != BuildStateComplete {
		t.Fatalf("result = %+v", res)
	}
	second := f.snapshot()[first:]
	if len(second) != 1 || second[0].URL != partURL(2) {
		t.Fatalf("resumed PUTs = %v", second)
	}
	if n := len(k.ASC.RequestsTo("POST", "/v1/buildUploads")); n != 1 {
		t.Errorf("buildUploads created %d times", n)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/apps/{id}/buildUploads")); n != 1 {
		t.Errorf("duplicate checks = %d", n)
	}
	patch := tooltest.JSON(t, k.ASC.RequestsTo("PATCH", "/v1/buildUploadFiles/{id}")[0].Body)
	if !strings.Contains(patch, `"sourceFileChecksums":{"file":{"algorithm":"MD5","hash":"`+res.Checksum+`"}}`) || !strings.Contains(patch, `"uploaded":true`) {
		t.Errorf("commit body = %s", patch)
	}
}

func TestUploadBuildDuplicate(t *testing.T) {
	k := tooltest.New(t)
	ipa := makeIPA(t, []byte(xmlInfoPlist))
	k.ASC.Handle("GET", "/v1/apps/{id}/buildUploads", http.StatusOK,
		`{"data":[{"type":"buildUploads","id":"old","attributes":{"state":{"state":"FAILED"}}},{"type":"buildUploads","id":"bu-9","attributes":{"state":{"state":"PROCESSING"}}}]}`)
	_, err := Build(writeCtx(t), DepsFromEnv(k.Env), ipaSpec(ipa))
	if !errors.Is(err, ErrDuplicateBuild) {
		t.Fatalf("err = %v", err)
	}
	mustContain(t, err, "bu-9")
	q := k.ASC.Requests()[0].Query
	if q.Get("filter[cfBundleVersion]") != "417" || q.Get("filter[cfBundleShortVersionString]") != "2.3.1" || q.Get("filter[platform]") != "IOS" {
		t.Errorf("query = %v", q)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Error("mutated on duplicate")
	}
}

func TestUploadBuildFailedAndArgs(t *testing.T) {
	k := tooltest.New(t)
	pkg := []byte("xar!pkg-bytes")
	ops := splitOps(int64(len(pkg)), 1)
	k.ASC.Handle("GET", "/v1/apps/{id}/buildUploads", http.StatusOK, `{"data":[]}`)
	k.ASC.Handle("POST", "/v1/buildUploads", http.StatusCreated, resourceJSON(t, "buildUploads", "bu-2", nil))
	k.ASC.Handle("POST", "/v1/buildUploadFiles", http.StatusCreated, resourceJSON(t, "buildUploadFiles", "buf-2", map[string]any{"uploadOperations": ops}))
	k.ASC.Handle("PATCH", "/v1/buildUploadFiles/{id}", http.StatusOK, resourceJSON(t, "buildUploadFiles", "buf-2", nil))
	k.ASC.Handle("GET", "/v1/buildUploads/{id}", http.StatusOK, resourceJSON(t, "buildUploads", "bu-2", map[string]any{
		"state": map[string]any{"state": "FAILED", "errors": []map[string]string{{"code": "90062", "description": "Invalid bundle version."}}},
	}))
	k.Fetch.Handle("PUT", partURL(0), http.StatusOK, nil)
	spec := BuildSpec{AppID: "app-1", FileName: "Hjul.pkg", File: bytes.NewReader(pkg), FileSize: int64(len(pkg))}
	_, err := Build(writeCtx(t), DepsFromEnv(k.Env), spec)
	if !errors.Is(err, tool.ErrInvalidArgs) {
		t.Fatalf("pkg without version: %v", err)
	}
	spec.Version, spec.BuildNumber = "1.0", "7"
	res, err := Build(writeCtx(t), DepsFromEnv(k.Env), spec)
	if !errors.Is(err, ErrUploadFailed) || res.State != BuildStateFailed || res.Platform != tool.MacOS {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	mustContain(t, err, "90062: Invalid bundle version.")
	create := tooltest.JSON(t, k.ASC.RequestsTo("POST", "/v1/buildUploadFiles")[0].Body)
	if !strings.Contains(create, `"uti":"com.apple.pkg"`) || !strings.Contains(create, `"assetType":"ASSET"`) {
		t.Errorf("file reservation = %s", create)
	}

	ipa := makeIPA(t, []byte(xmlInfoPlist))
	s := ipaSpec(ipa)
	s.BuildNumber = "999"
	_, err = Build(writeCtx(t), DepsFromEnv(k.Env), s)
	mustContain(t, err, "contains 2.3.1 (417)")
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateImage(t *testing.T) {
	opaque := image.NewRGBA(image.Rect(0, 0, 1280, 800))
	for i := range opaque.Pix {
		opaque.Pix[i] = 0xFF
	}
	alpha := image.NewNRGBA(image.Rect(0, 0, 1280, 800))
	pal := image.NewPaletted(image.Rect(0, 0, 1280, 800), color.Palette{color.NRGBA{0, 0, 0, 0}, color.White})
	var jpg, gf bytes.Buffer
	if err := jpeg.Encode(&jpg, opaque, nil); err != nil {
		t.Fatal(err)
	}
	opaquePNG := encodePNG(t, opaque)
	if err := gif.Encode(&gf, image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.White}), nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		data    []byte
		display applemeta.DisplayType
		want    string
	}{
		{"opaque png", opaquePNG, applemeta.AppDesktop, ""},
		{"jpeg", jpg.Bytes(), applemeta.AppDesktop, ""},
		{"alpha png", encodePNG(t, alpha), applemeta.AppDesktop, "alpha channel"},
		{"trns png", encodePNG(t, pal), applemeta.AppDesktop, "tRNS"},
		{"wrong size", opaquePNG, applemeta.AppIPhone65, "not an accepted size"},
		{"gif", gf.Bytes(), applemeta.AppDesktop, "PNG or JPEG"},
		{"unknown display", opaquePNG, "APP_FRIDGE", "unknown display"},
		{"truncated", opaquePNG[:20], applemeta.AppDesktop, "png header"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateImage(bytes.NewReader(c.data), c.display)
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			mustContain(t, err, c.want)
			if !errors.Is(err, tool.ErrInvalidArgs) {
				t.Errorf("not invalid_arguments: %v", err)
			}
		})
	}
}

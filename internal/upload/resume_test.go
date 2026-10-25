package upload

import (
	"errors"
	"net/http"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// reserveFixture registers the post-create half of the build flow for buf-1.
func reserveFixture(t *testing.T, k *tooltest.Kit, size int64, firstReserve int) []Operation {
	t.Helper()
	ops := splitOps(size, 1)
	file := resourceJSON(t, "buildUploadFiles", "buf-1", map[string]any{"uploadOperations": ops})
	if firstReserve != 0 {
		k.ASC.Handle("POST", "/v1/buildUploadFiles", firstReserve,
			`{"errors":[{"status":"422","code":"ENTITY_ERROR","title":"bad"}]}`)
	}
	k.ASC.Handle("POST", "/v1/buildUploadFiles", http.StatusCreated, file)
	k.ASC.Handle("PATCH", "/v1/buildUploadFiles/{id}", http.StatusOK, resourceJSON(t, "buildUploadFiles", "buf-1", nil))
	k.Fetch.Handle("PUT", partURL(0), http.StatusOK, nil)
	return ops
}

func uploadJSON(t *testing.T, id, state string) string {
	return resourceJSON(t, "buildUploads", id, map[string]any{"state": map[string]any{"state": state}})
}

// TU-1: the buildUpload id is saved before reserving its file, so a failed
// reservation resumes on that buildUpload instead of reporting a duplicate.
func TestUploadBuildResumesInterruptedReservation(t *testing.T) {
	k := tooltest.New(t)
	ipa := makeIPA(t, []byte(xmlInfoPlist))
	k.ASC.Handle("GET", "/v1/apps/{id}/buildUploads", http.StatusOK, `{"data":[]}`)
	k.ASC.Handle("POST", "/v1/buildUploads", http.StatusCreated, uploadJSON(t, "bu-1", BuildStateAwaitingUpload))
	reserveFixture(t, k, int64(len(ipa)), http.StatusUnprocessableEntity)
	k.ASC.Handle("GET", "/v1/buildUploads/{id}", http.StatusOK, uploadJSON(t, "bu-1", BuildStateAwaitingUpload))
	k.ASC.Handle("GET", "/v1/buildUploads/{id}", http.StatusOK, uploadJSON(t, "bu-1", BuildStateComplete))
	ctx := writeCtx(t)
	_, err := Build(ctx, DepsFromEnv(k.Env), ipaSpec(ipa))
	mustContain(t, err, "buildUpload bu-1: reserve file (call again with the same file to resume)")

	res, err := Build(ctx, DepsFromEnv(k.Env), ipaSpec(ipa))
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildUploadID != "bu-1" || !res.Resumed || res.State != BuildStateComplete {
		t.Fatalf("result = %+v", res)
	}
	if n := len(k.ASC.RequestsTo("POST", "/v1/buildUploads")); n != 1 {
		t.Errorf("buildUploads created %d times", n)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/apps/{id}/buildUploads")); n != 1 {
		t.Errorf("duplicate checks = %d", n)
	}
	if n := len(k.ASC.RequestsTo("POST", "/v1/buildUploadFiles")); n != 2 {
		t.Errorf("file reservations = %d", n)
	}
}

// TU-1/TU-2: an AWAITING_UPLOAD buildUpload of the same version/build with no
// file reserved (no resume record, e.g. a re-exported archive) is taken over.
func TestUploadBuildTakesOverUnreservedUpload(t *testing.T) {
	k := tooltest.New(t)
	ipa := makeIPA(t, []byte(xmlInfoPlist))
	k.ASC.Handle("GET", "/v1/apps/{id}/buildUploads", http.StatusOK,
		`{"data":[{"type":"buildUploads","id":"bu-7","attributes":{"state":{"state":"AWAITING_UPLOAD"}}}]}`)
	k.ASC.Handle("GET", "/v1/buildUploads/{id}/buildUploadFiles", http.StatusOK, `{"data":[]}`)
	reserveFixture(t, k, int64(len(ipa)), 0)
	k.ASC.Handle("GET", "/v1/buildUploads/{id}", http.StatusOK, uploadJSON(t, "bu-7", BuildStateComplete))
	res, err := Build(writeCtx(t), DepsFromEnv(k.Env), ipaSpec(ipa))
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildUploadID != "bu-7" || !res.Resumed {
		t.Fatalf("result = %+v", res)
	}
	if n := len(k.ASC.RequestsTo("POST", "/v1/buildUploads")); n != 0 {
		t.Errorf("buildUploads created %d times", n)
	}
	body := tooltest.JSON(t, k.ASC.RequestsTo("POST", "/v1/buildUploadFiles")[0].Body)
	mustContain(t, errors.New(body), `"id":"bu-7"`)
}

// TU-2: an AWAITING_UPLOAD buildUpload whose file another run reserved gets a
// distinct error naming it and the fix, not "wait for it to finish".
func TestUploadBuildStaleReservedUpload(t *testing.T) {
	k := tooltest.New(t)
	ipa := makeIPA(t, []byte(xmlInfoPlist))
	k.ASC.Handle("GET", "/v1/apps/{id}/buildUploads", http.StatusOK,
		`{"data":[{"type":"buildUploads","id":"bu-7","attributes":{"state":{"state":"AWAITING_UPLOAD"}}}]}`)
	k.ASC.Handle("GET", "/v1/buildUploads/{id}/buildUploadFiles", http.StatusOK,
		`{"data":[{"type":"buildUploadFiles","id":"buf-old","attributes":{"fileName":"Old.ipa"}}]}`)
	_, err := Build(writeCtx(t), DepsFromEnv(k.Env), ipaSpec(ipa))
	if !errors.Is(err, ErrStaleBuildUpload) || errors.Is(err, ErrDuplicateBuild) {
		t.Fatalf("err = %v", err)
	}
	mustContain(t, err, "bu-7")
	if len(k.ASC.Mutations()) != 0 {
		t.Error("mutated on stale upload")
	}
}

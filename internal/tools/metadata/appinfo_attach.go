package metadata

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/upload"
)

// attachIn adds or removes App Review attachments of the version's review
// detail. Removing is irreversible, so it is listed in the confirm preview.
type attachIn struct {
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// planAttachments plans attachment uploads and deletions on review detail
// detailID. Files are opened through env.Files, which enforces the allowed
// roots and refuses symlinks, both now and again at upload time.
func planAttachments(ctx context.Context, env *tool.Env, versionID, detailID string, in *attachIn) ([]infoWrite, []infoChange, error) {
	if len(in.Add) == 0 && len(in.Remove) == 0 {
		return nil, nil, nil
	}
	if detailID == "" {
		return nil, nil, tool.WithNext(tool.InvalidArgs("version has no App Review detail yet; set review contact first"),
			tool.NextStep{Tool: "appinfo_update", Args: map[string]any{"review": map[string]any{"contact_email": "<email>"}}})
	}
	var (
		writes  []infoWrite
		changes []infoChange
	)
	for _, p := range in.Add {
		size, err := regularSize(env, p)
		if err != nil {
			return nil, nil, err
		}
		name := filepath.Base(p)
		changes = append(changes, infoChange{Target: "appStoreReviewAttachment", Field: "add", Current: nil, New: name})
		writes = append(writes, infoWrite{label: "appStoreReviewAttachment", attach: p, run: func(ctx context.Context) (string, error) {
			return uploadAttachment(ctx, env, detailID, p, name, size)
		}})
	}
	if len(in.Remove) > 0 {
		res, err := env.ASC.List(ctx, "/v1/appStoreReviewDetails/"+url.PathEscape(detailID)+"/appStoreReviewAttachments",
			tool.ListOptions{Fields: map[string][]string{"appStoreReviewAttachments": {"fileName"}}, MaxItems: 200})
		if err != nil {
			return nil, nil, fmt.Errorf("read review attachments: %w", err)
		}
		for _, id := range in.Remove {
			i := slices.IndexFunc(res.Data, func(r tool.Resource) bool { return r.ID == id })
			if i < 0 {
				return nil, nil, tool.InvalidArgs("attachment %s is not on version %s's review detail", id, versionID)
			}
			changes = append(changes, infoChange{
				Target: "appStoreReviewAttachment", ID: id, Field: "remove",
				Current: res.Data[i].Text("fileName"), New: nil,
			})
			writes = append(writes, infoWrite{label: "appStoreReviewAttachment", attach: id, req: tool.Request{
				Method: http.MethodDelete, Path: "/v1/appStoreReviewAttachments/" + url.PathEscape(id), Write: true,
			}})
		}
	}
	return writes, changes, nil
}

// withoutDone returns in with the attachment paths and ids in done dropped,
// so a suggested re-run never uploads or deletes them twice.
func withoutDone(in appinfoIn, done map[string]bool) appinfoIn {
	if in.ReviewAttachments == nil || len(done) == 0 {
		return in
	}
	keep := func(xs []string) []string {
		return slices.DeleteFunc(slices.Clone(xs), func(x string) bool { return done[x] })
	}
	in.ReviewAttachments = &attachIn{Add: keep(in.ReviewAttachments.Add), Remove: keep(in.ReviewAttachments.Remove)}
	if len(in.ReviewAttachments.Add)+len(in.ReviewAttachments.Remove) == 0 {
		in.ReviewAttachments = nil
	}
	return in
}

// regularSize checks that p opens under the allowed roots and is a regular
// non-empty file, returning its size.
func regularSize(env *tool.Env, p string) (int64, error) {
	fh, err := env.Files.Open(p)
	if err != nil {
		return 0, fmt.Errorf("%w: file: %v", tool.ErrInvalidArgs, err)
	}
	defer func() { _ = fh.Close() }()
	st, err := fh.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", p, err)
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return 0, fmt.Errorf("%w: %s is not a non-empty regular file", tool.ErrInvalidArgs, p)
	}
	return st.Size(), nil
}

// uploadAttachment reserves, uploads and commits one attachment.
func uploadAttachment(ctx context.Context, env *tool.Env, detailID, p, name string, size int64) (string, error) {
	fh, err := env.Files.Open(p)
	if err != nil {
		return "", fmt.Errorf("%w: file: %v", tool.ErrInvalidArgs, err)
	}
	defer func() { _ = fh.Close() }()
	if st, err := fh.Stat(); err != nil || st.Size() != size {
		return "", fmt.Errorf("%w: %s changed since the preview", tool.ErrInvalidArgs, p)
	}
	res, err := upload.Asset(ctx, upload.DepsFromEnv(env), upload.AssetSpec{
		CreatePath: "/v1/appStoreReviewAttachments", Type: "appStoreReviewAttachments",
		Parent:   upload.Relationship{Name: "appStoreReviewDetail", Type: "appStoreReviewDetails", ID: detailID},
		FileName: name, FileSize: size, File: fh,
	})
	if res != nil {
		return res.ID, err
	}
	return "", err
}

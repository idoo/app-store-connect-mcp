package upload

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// Build upload states (BuildUploadState).
const (
	BuildStateAwaitingUpload = "AWAITING_UPLOAD"
	BuildStateProcessing     = "PROCESSING"
	BuildStateFailed         = "FAILED"
	BuildStateComplete       = "COMPLETE"
)

// resumeKind is the StateStore kind of build-upload resume records.
const resumeKind = "uploads"

// maxInfoPlist bounds the Info.plist read from an IPA.
const maxInfoPlist = 4 << 20

// ErrDuplicateBuild reports that the version/build pair was already uploaded.
var ErrDuplicateBuild = &tool.Error{
	Code:    "duplicate_build",
	Message: "this version and build number were already uploaded",
	Hint:    "increase CFBundleVersion and rebuild, or wait for the existing upload to finish",
}

// BuildSpec describes an IPA or PKG upload via the BuildUpload API. The
// caller opens File through the confined filesystem.
type BuildSpec struct {
	AppID string
	// Platform defaults to IOS for .ipa and MAC_OS for .pkg.
	Platform tool.Platform
	// FileName is the base name; its extension (.ipa or .pkg) selects the UTI.
	FileName string
	File     io.ReaderAt
	FileSize int64
	// Version and BuildNumber (CFBundleShortVersionString, CFBundleVersion)
	// are read from an IPA's Info.plist; when also given they must match.
	// They are required for .pkg.
	Version     string
	BuildNumber string
	Poll        PollOptions
}

// BuildResult is the outcome of a build upload.
type BuildResult struct {
	BuildUploadID string        `json:"build_upload_id"`
	FileID        string        `json:"build_upload_file_id"`
	Version       string        `json:"version"`
	BuildNumber   string        `json:"build_number"`
	Platform      tool.Platform `json:"platform"`
	State         string        `json:"state"`
	Checksum      string        `json:"md5"`
	Errors        []StateDetail `json:"errors,omitempty"`
	Warnings      []StateDetail `json:"warnings,omitempty"`
	Infos         []StateDetail `json:"infos,omitempty"`
	// Resumed is true when an interrupted upload of the same file continued.
	Resumed bool `json:"resumed,omitempty"`
	// PartsSkipped counts parts already uploaded by an earlier attempt.
	PartsSkipped int `json:"parts_skipped,omitempty"`
	// StillProcessing is true when the wait ended before a terminal state;
	// calling Build again with the same file resumes polling.
	StillProcessing bool `json:"still_processing,omitempty"`
}

// partRange identifies an uploaded byte range.
type partRange struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
}

// resumeRecord is persisted under resumeKind/<sha256 of the file>. It holds
// ids and progress only, never presigned URLs.
type resumeRecord struct {
	AppID         string        `json:"app_id"`
	Platform      tool.Platform `json:"platform"`
	Version       string        `json:"version"`
	BuildNumber   string        `json:"build_number"`
	MD5           string        `json:"md5"`
	BuildUploadID string        `json:"build_upload_id"`
	FileID        string        `json:"build_upload_file_id"`
	Done          []partRange   `json:"done,omitempty"`
	Committed     bool          `json:"committed,omitempty"`
}

// buildState is the BuildUpload.state attribute.
type buildState struct {
	State    string        `json:"state"`
	Errors   []StateDetail `json:"errors"`
	Warnings []StateDetail `json:"warnings"`
	Infos    []StateDetail `json:"infos"`
}

// Build runs the build flow: duplicate check, POST buildUploads, POST
// buildUploadFiles, PUT parts, PATCH the file with uploaded and its MD5
// sourceFileChecksums, then poll the buildUpload until COMPLETE or FAILED.
// Progress is saved in d.State keyed by the file's SHA-256, so a repeated
// call resumes after the last uploaded part. When Apple reports FAILED the
// result is returned together with a [*DeliveryError].
func Build(ctx context.Context, d Deps, s BuildSpec) (*BuildResult, error) {
	uti, err := s.normalize()
	if err != nil {
		return nil, err
	}
	sum, key, err := fileDigests(io.NewSectionReader(s.File, 0, s.FileSize))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.FileName, err)
	}
	out := &BuildResult{Version: s.Version, BuildNumber: s.BuildNumber, Platform: s.Platform, Checksum: sum, State: BuildStateAwaitingUpload}
	rec := resumeRecord{AppID: s.AppID, Platform: s.Platform, Version: s.Version, BuildNumber: s.BuildNumber, MD5: sum}
	save := func() error {
		if d.State == nil {
			return nil
		}
		if err := d.State.Save(resumeKind, key, rec); err != nil {
			return fmt.Errorf("save upload state: %w", err)
		}
		return nil
	}

	var ops []Operation
	if prev, ok, err := loadResume(d, key, rec); err != nil {
		return nil, err
	} else if ok {
		rec = prev
		if !rec.Committed {
			file, err := d.ASC.Get(ctx, "/v1/buildUploadFiles/"+url.PathEscape(rec.FileID), nil)
			switch {
			case tool.IsNotFound(err):
				rec = resumeRecord{AppID: s.AppID, Platform: s.Platform, Version: s.Version, BuildNumber: s.BuildNumber, MD5: sum}
			case err != nil:
				return nil, fmt.Errorf("resume buildUploadFiles %s: %w", rec.FileID, err)
			default:
				res, _ := file.First()
				if ops, err = ParseOperations(res); err != nil {
					return nil, fmt.Errorf("resume: %w", err)
				}
			}
		}
		out.Resumed = rec.BuildUploadID != ""
	}

	if rec.BuildUploadID == "" {
		if err := checkDuplicate(ctx, d, s); err != nil {
			return nil, err
		}
		if rec.BuildUploadID, rec.FileID, ops, err = reserveBuild(ctx, d, s, uti); err != nil {
			return nil, err
		}
		if err := save(); err != nil {
			return nil, err
		}
	}
	out.BuildUploadID, out.FileID = rec.BuildUploadID, rec.FileID
	pending := func(err error) error {
		return fmt.Errorf("buildUpload %s not completed (call again with the same file to resume): %w", rec.BuildUploadID, err)
	}

	if !rec.Committed {
		done := map[partRange]bool{}
		for _, p := range rec.Done {
			done[p] = true
		}
		skip := map[int]bool{}
		for i, op := range ops {
			if done[partRange{op.Offset, op.Length}] {
				skip[i] = true
			}
		}
		out.PartsSkipped = len(skip)
		err := putParts(ctx, partsJob{
			fetch: d.Fetch, clock: d.clock(), file: s.File, size: s.FileSize, ops: ops,
			progress: d.Progress, skip: skip,
			onDone: func(i int) error {
				rec.Done = append(rec.Done, partRange{ops[i].Offset, ops[i].Length})
				return save()
			},
		})
		if err != nil {
			return nil, pending(err)
		}
		_, err = d.ASC.Do(ctx, tool.Request{
			Method: http.MethodPatch, Path: "/v1/buildUploadFiles/" + url.PathEscape(rec.FileID), Write: true,
			Body: resourceBody("buildUploadFiles", rec.FileID, map[string]any{
				"uploaded":            true,
				"sourceFileChecksums": map[string]any{"file": map[string]string{"hash": sum, "algorithm": "MD5"}},
			}, nil),
		})
		if err != nil {
			return nil, pending(fmt.Errorf("commit: %w", err))
		}
		rec.Committed, rec.Done = true, nil
		if err := save(); err != nil {
			return nil, err
		}
	}

	uploadPath := "/v1/buildUploads/" + url.PathEscape(rec.BuildUploadID)
	first, err := d.ASC.Get(ctx, uploadPath, nil)
	if err != nil {
		return nil, pending(fmt.Errorf("read state: %w", err))
	}
	res, _ := first.First()
	still, err := poll(ctx, d, uploadPath, s.Poll, res, func(r tool.Resource) (bool, error) {
		var st buildState
		if err := decodeAttr(r, "state", &st); err != nil {
			return false, err
		}
		if st.State != "" {
			out.State = st.State
		}
		out.Errors, out.Warnings, out.Infos = st.Errors, st.Warnings, st.Infos
		return st.State == BuildStateComplete || st.State == BuildStateFailed, nil
	})
	if err != nil {
		return nil, pending(fmt.Errorf("poll: %w", err))
	}
	out.StillProcessing = still
	if !still && d.State != nil {
		if err := d.State.Delete(resumeKind, key); err != nil {
			return nil, fmt.Errorf("clear upload state: %w", err)
		}
	}
	if out.State == BuildStateFailed {
		return out, &DeliveryError{Type: "buildUploads", ID: rec.BuildUploadID, Errors: out.Errors}
	}
	return out, nil
}

// normalize fills Platform and the bundle versions and returns the UTI.
func (s *BuildSpec) normalize() (string, error) {
	switch {
	case s.AppID == "":
		return "", fmt.Errorf("%w: app id is required", tool.ErrInvalidArgs)
	case s.FileName == "" || strings.ContainsAny(s.FileName, `/\`):
		return "", fmt.Errorf("%w: file name %q", tool.ErrInvalidArgs, s.FileName)
	case s.FileSize <= 0 || s.File == nil:
		return "", fmt.Errorf("%w: %s is empty", tool.ErrInvalidArgs, s.FileName)
	}
	var uti string
	switch strings.ToLower(path.Ext(s.FileName)) {
	case ".ipa":
		uti = "com.apple.ipa"
		if s.Platform == "" {
			s.Platform = tool.IOS
		}
		version, build, err := IPAVersions(s.File, s.FileSize)
		if err != nil {
			return "", fmt.Errorf("%s: %w", s.FileName, err)
		}
		if (s.Version != "" && s.Version != version) || (s.BuildNumber != "" && s.BuildNumber != build) {
			return "", fmt.Errorf("%w: %s contains %s (%s), not %s (%s)", tool.ErrInvalidArgs, s.FileName, version, build, s.Version, s.BuildNumber)
		}
		s.Version, s.BuildNumber = version, build
	case ".pkg":
		uti = "com.apple.pkg"
		if s.Platform == "" {
			s.Platform = tool.MacOS
		}
		if s.Version == "" || s.BuildNumber == "" {
			return "", fmt.Errorf("%w: a .pkg upload needs version and build number", tool.ErrInvalidArgs)
		}
	default:
		return "", fmt.Errorf("%w: %s is not an .ipa or .pkg", tool.ErrInvalidArgs, s.FileName)
	}
	return uti, nil
}

// IPAVersions reads CFBundleShortVersionString and CFBundleVersion from the
// Payload/<name>.app/Info.plist of an IPA (binary or XML plist).
func IPAVersions(r io.ReaderAt, size int64) (version, build string, err error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return "", "", fmt.Errorf("read ipa: %w", err)
	}
	var info *zip.File
	for _, f := range zr.File {
		parts := strings.Split(f.Name, "/")
		if len(parts) == 3 && parts[0] == "Payload" && strings.HasSuffix(parts[1], ".app") && parts[2] == "Info.plist" {
			if info != nil {
				return "", "", fmt.Errorf("read ipa: several apps in Payload/")
			}
			info = f
		}
	}
	if info == nil {
		return "", "", fmt.Errorf("read ipa: no Payload/*.app/Info.plist")
	}
	if info.UncompressedSize64 > maxInfoPlist {
		return "", "", fmt.Errorf("read ipa: Info.plist larger than %d bytes", maxInfoPlist)
	}
	rc, err := info.Open()
	if err != nil {
		return "", "", fmt.Errorf("read ipa: %w", err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxInfoPlist+1))
	if err != nil {
		return "", "", fmt.Errorf("read ipa Info.plist: %w", err)
	}
	if len(b) > maxInfoPlist {
		return "", "", fmt.Errorf("read ipa: Info.plist larger than %d bytes", maxInfoPlist)
	}
	v, err := ParsePlist(b)
	if err != nil {
		return "", "", fmt.Errorf("read ipa Info.plist: %w", err)
	}
	dict, ok := v.(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("read ipa: Info.plist is not a dictionary")
	}
	version, _ = dict["CFBundleShortVersionString"].(string)
	build, _ = dict["CFBundleVersion"].(string)
	if version == "" || build == "" {
		return "", "", fmt.Errorf("read ipa: Info.plist lacks CFBundleShortVersionString or CFBundleVersion")
	}
	return version, build, nil
}

// loadResume returns a stored record matching want's identity.
func loadResume(d Deps, key string, want resumeRecord) (resumeRecord, bool, error) {
	if d.State == nil {
		return resumeRecord{}, false, nil
	}
	var rec resumeRecord
	ok, err := d.State.Load(resumeKind, key, &rec)
	if err != nil {
		return resumeRecord{}, false, fmt.Errorf("load upload state: %w", err)
	}
	if !ok || rec.BuildUploadID == "" || rec.FileID == "" || rec.AppID != want.AppID || rec.Platform != want.Platform ||
		rec.Version != want.Version || rec.BuildNumber != want.BuildNumber || rec.MD5 != want.MD5 {
		return resumeRecord{}, false, nil
	}
	return rec, true, nil
}

// checkDuplicate refuses a version/build already uploaded and not FAILED.
func checkDuplicate(ctx context.Context, d Deps, s BuildSpec) error {
	q := url.Values{}
	q.Set("filter[cfBundleVersion]", s.BuildNumber)
	q.Set("filter[cfBundleShortVersionString]", s.Version)
	q.Set("filter[platform]", string(s.Platform))
	list, err := d.ASC.List(ctx, "/v1/apps/"+url.PathEscape(s.AppID)+"/buildUploads", tool.ListOptions{Query: q, MaxItems: 50})
	if err != nil {
		return fmt.Errorf("check existing build uploads: %w", err)
	}
	for _, r := range list.Data {
		var st buildState
		if err := decodeAttr(r, "state", &st); err != nil {
			return err
		}
		if st.State != BuildStateFailed {
			return fmt.Errorf("%w: %s (%s) is buildUpload %s in state %s", ErrDuplicateBuild, s.Version, s.BuildNumber, r.ID, st.State)
		}
	}
	return nil
}

// reserveBuild creates the buildUpload and its buildUploadFile.
func reserveBuild(ctx context.Context, d Deps, s BuildSpec, uti string) (uploadID, fileID string, ops []Operation, err error) {
	resp, err := d.ASC.Do(ctx, tool.Request{
		Method: http.MethodPost, Path: "/v1/buildUploads", Write: true,
		Body: resourceBody("buildUploads", "", map[string]any{
			"cfBundleShortVersionString": s.Version,
			"cfBundleVersion":            s.BuildNumber,
			"platform":                   string(s.Platform),
		}, map[string]Relationship{"app": {Type: "apps", ID: s.AppID}}),
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("create buildUpload: %w", err)
	}
	up, ok := resp.First()
	if !ok || up.ID == "" {
		return "", "", nil, fmt.Errorf("create buildUpload: response has no resource")
	}
	resp, err = d.ASC.Do(ctx, tool.Request{
		Method: http.MethodPost, Path: "/v1/buildUploadFiles", Write: true,
		Body: resourceBody("buildUploadFiles", "", map[string]any{
			"fileName": s.FileName, "fileSize": s.FileSize, "uti": uti, "assetType": "ASSET",
		}, map[string]Relationship{"buildUpload": {Type: "buildUploads", ID: up.ID}}),
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("buildUpload %s created; reserve file: %w", up.ID, err)
	}
	file, ok := resp.First()
	if !ok || file.ID == "" {
		return "", "", nil, fmt.Errorf("buildUpload %s created; reserve file: response has no resource", up.ID)
	}
	if ops, err = ParseOperations(file); err != nil {
		return "", "", nil, fmt.Errorf("buildUpload %s: %w", up.ID, err)
	}
	return up.ID, file.ID, ops, nil
}

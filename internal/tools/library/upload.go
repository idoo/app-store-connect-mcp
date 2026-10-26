package library

import (
	"context"
	"fmt"

	"github.com/idoo/app-store-connect-mcp/internal/upload"
)

// upload reserves, uploads and commits one library asset (no
// sourceFileChecksum: Apple's library assets have none), then polls the flat
// state until PREPARE_FOR_SUBMISSION or FAILED.
func (a *applier) upload(ctx context.Context, u uploadOut) (*upload.AssetResult, error) {
	fh, err := a.env.Files.Open(u.path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", u.File, err)
	}
	defer func() { _ = fh.Close() }()
	path, typ := "/v1/appAssetLibraryImages", "appAssetLibraryImages"
	if u.Kind == "video" {
		path, typ = "/v1/appAssetLibraryVideos", "appAssetLibraryVideos"
	}
	deps := upload.DepsFromEnv(a.env)
	return upload.Asset(ctx, deps, upload.AssetSpec{
		CreatePath: path, Type: typ,
		Parent:   upload.Relationship{Name: "assetLibrary", Type: "appAssetLibraries", ID: a.p.libraryID},
		FileName: u.info.Name, FileSize: u.info.Size, File: fh,
		Attributes: map[string]any{"category": u.Category},
		NoChecksum: true,
		StateField: "state",
	})
}

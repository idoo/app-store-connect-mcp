---
name: asc-mcp-screenshots
description: Use when the user asks to upload, replace, reorder or check App Store screenshots or app previews, e.g. after fastlane snapshot, via the asc-mcp App Store Connect MCP server ("upload the new screenshots for Hjul", "sync fastlane/screenshots to App Store Connect").
---

# Screenshots with asc-mcp

Sync a local screenshot folder to the editable version. Every upload, delete and reorder gets previewed first.

Refs: `app` is a name, bundle id, SKU or id (omit it if pinned). `version` is `"editable"` (default). `dir` is a folder inside the project, laid out as `<locale>/*.png` (fastlane; the display type comes from the pixel size) or `<locale>/<display>/*.png`. `locales` limits the sync, e.g. `["en-US", "de-DE"]`.

## Steps

1. If `screenshots_list` is missing (discovery mode, or someone turned it off), call `toolsets` with `action: "enable"` and `toolsets: ["screenshots"]`.
2. Optional: make the images first (`fastlane snapshot`) and check the folder layout.
3. `screenshots_list` with `app` and `version` shows current sets per locale and display. `thumbnails: true` attaches up to 4 small images.
4. `screenshots_plan` with `app`, `version`, `dir`, `mode`. `mode: "replace"` (default) makes each touched set exactly the local files and deletes the extras. `mode: "add"` only uploads what's missing. It checks size, format and alpha, skips matching checksums, and returns a `plan_id` with uploads, deletes, reorders and problems.
5. Fix every problem (wrong size, alpha channel, a size that needs a `<display>` subfolder) and plan again until it's clean.
6. Show the plan. Deletes are the risky part: list them and ask.
7. `screenshots_sync` with the same `app`, `version`, `dir`, `mode`, `locales` plus `plan_id`. `wait_seconds` waits for Apple's processing.
8. `screenshots_list` to check every asset is COMPLETE, then `release_status` to see the screenshot check pass.

App previews: `previews_upload` with `app`, `locale`, `file` (a video inside the project) and optional `preview_frame_time_code`. Same checksum already there does nothing. Max 3 per set.

## Rules

- `stale_plan`: remote sets changed. Plan again and show it again.
- `missing_localization`: the version has no listing for that locale yet. Add one with `metadata_plan` / `metadata_apply` first (see `asc-mcp-metadata-translate`).
- `no_editable_version`: create the version first (`release_status` with `desired`, then `release_prepare`).
- `already` means unchanged. That's success.
- Files are read only from inside the project root. Don't point `dir` at system or home folders.
- Never ask for an API key, .p8 or JWT in chat. Keys come from `asc-mcp auth add`.
- Don't upload or delete through `asc_api_request` what `screenshots_sync` refused.

## When it fails

- `writes_disabled`: `toolsets` with `action: "enable_writes"`; the user approves. `writes_locked`: tell the user; don't bypass it.
- `upload_failed`: show Apple's per-file errors, fix the file, plan again.
- `outcome_unknown`: `screenshots_list` before retrying anything.

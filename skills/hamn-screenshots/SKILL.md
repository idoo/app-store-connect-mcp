---
name: hamn-screenshots
description: Use when the user asks to upload, replace, reorder or check App Store screenshots or app previews, e.g. after fastlane snapshot, via the hamn App Store Connect MCP server ("upload the new screenshots for Hjul", "sync fastlane/screenshots to App Store Connect").
---

# Screenshots with hamn

Goal: sync a local screenshot folder (fastlane snapshot output or hand-made
frames) to the editable App Store version, previewing every upload, delete and
reorder first. Requires the `hamn` MCP server (`hamn` binary on PATH, key set
up by the human). The tools live in the optional `screenshots` toolset.

## Refs you can pass
- `app`: name ("Hjul"), bundle id, SKU or id; omit when the project pins one.
- `version`: `"editable"` (default).
- `dir`: a folder inside the project, laid out `<locale>/*.png` (fastlane;
  display type inferred from pixel size) or `<locale>/<display>/*.png`.
- `locales`: limit to some locales, e.g. `["en-US", "de-DE"]`.

## Tool chain
1. If `screenshots_list` is not available, call `toolsets` with
   `action: "enable"` and `toolsets: ["screenshots"]`.
2. Optional: produce images first (for example `fastlane snapshot`), and check
   the folder layout matches the refs above.
3. `screenshots_list` with `app` and `version` to see current sets per locale
   and display type (`thumbnails: true` attaches up to 4 small images).
4. `screenshots_plan` with `app`, `version`, `dir` and `mode`:
   - `mode: "add"` only uploads missing screenshots;
   - `mode: "replace"` (default) makes each touched set exactly the local
     files, deleting extra remote screenshots.
   It validates size, format and alpha, skips files whose checksum already
   matches, and returns a `plan_id` with uploads, deletes, reorders and
   problems.
5. Fix every problem (wrong pixel size, alpha channel, ambiguous size needing a
   `<display>` subfolder) and plan again until it is clean.
6. Show the plan. Deletes are the risky part: list them explicitly and ask.
7. `screenshots_sync` with the same `app`, `version`, `dir`, `mode` and
   `locales` plus `plan_id` set to the plan's `plan_id`. Long syncs report progress;
   pass `wait_seconds` to wait for Apple's processing.
8. `screenshots_list` again to confirm every asset is COMPLETE, then
   `release_status` to see the screenshot check pass.

## App previews
- `previews_upload` with `app`, `locale`, `file` (a video inside the project)
  and optional `preview_frame_time_code`. Same checksum already present is a
  no-op; max 3 previews per set.

## Decision points
- `stale_plan`: remote sets changed since planning; plan again and re-show.
- `missing_localization`: the version has no listing for that locale yet; add
  it with `metadata_plan` / `metadata_apply` first (see hamn-metadata-translate).
- `no_editable_version`: create the version first (`release_status` with
  `desired`, then `release_prepare`).
- `already` items are unchanged; that is success.

## Safety rules
- Files are read only from inside the project root; never point `dir` at
  system or home folders.
- Never ask the user to paste an API key, .p8 contents or JWT into chat; keys
  are configured by the human with `hamn auth add`.
- Never use `asc_api_request` to delete or upload what `screenshots_sync`
  refused.

## Failure handling
- `writes_disabled`: call `toolsets` with `action: "enable_writes"`; the user
  approves in their client. `writes_locked`: tell the user; do not bypass.
- `upload_failed`: show Apple's per-file errors, fix the file, plan again.
- `outcome_unknown`: run `screenshots_list` before retrying anything.

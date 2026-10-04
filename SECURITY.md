# Security policy

Only the latest release gets fixes, so upgrade before reporting.

## Reporting

Use [private vulnerability reporting](https://github.com/idoo/hamn/security/advisories/new), not a public issue. It's one person on the other end, so expect a reply within a few days and a fix or plan within two weeks for anything serious. You get credit in the advisory unless you'd rather not.

## In scope

- Getting a write, public or destructive action past the preview and approval, via tool input, Apple text or a repo file.
- Keys, JWTs or signed URLs leaking into results, errors or logs.
- The JWT going anywhere but the App Store Connect API.
- Getting around the URL policy, cursor checks, file confinement, or the HTTP token, Origin and loopback checks.
- Broken release artifacts, checksums or provenance.

## Out of scope

- A model fooled by injected text *and* a human clicking yes. See [docs/SAFETY.md](docs/SAFETY.md).
- Setups that switch safety off on purpose: auto-approve everything, `allow_no_elicit`, `--no-confirm` with `HAMN_CI=1`.
- Processes running as you reading your keychain or key files.
- Apple's own API behaviour.

## Keys

Hamn reads keys from a file, an env var or the OS keychain. The config stores paths or keychain references, never the key. Only `hamn auth` can change credentials. JWTs are signed locally, cached in memory for at most 19 minutes, sent only to `api.appstoreconnect.apple.com` and redacted from everything Hamn prints.

No telemetry. Hamn talks to the App Store Connect API, to the presigned URLs Apple hands back (without auth), and to the public iTunes Search API for `store_lookup`. `hamn auth add`/`import` make one test call before saving, and `hamn config install claude-code` runs your local `claude`.

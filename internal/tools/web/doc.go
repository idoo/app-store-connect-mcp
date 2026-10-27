// Package web registers the web toolset: App Store Connect features the
// public API lacks (creating app records, App Privacy), called through the
// human's Apple ID web session ([tool.Env.Web], the iris JSON:API). The
// human signs in with `app-store-connect-mcp web login`, or in the browser
// window the web_login tool opens ([tool.Env.WebLogin]); no tool ever sees
// Apple ID credentials or cookie values.
//
// Protocol details follow rudrankriyam/App-Store-Connect-CLI (MIT):
// internal/web/apps.go (POST /iris/v1/apps), internal/web/privacy.go and
// docs/design/web-privacy-apply-resilience.md (appDataUsages).
package web

// Package websession holds the human's App Store Connect web session (Apple
// ID sign-in): the cookies the browser would send to appstoreconnect.apple.com.
// It powers the few features Apple keeps out of the public API (creating app
// records, App Privacy). A session comes from a browser window with a
// dedicated profile ([BrowserLogin]), the terminal password flow ([Login]) or
// pasted cookies ([Import]); MCP tools only use it and never see Apple ID
// credentials.
package websession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Apple endpoints shared by login, import and validation.
const (
	// ASCOrigin is the App Store Connect web origin.
	ASCOrigin = "https://appstoreconnect.apple.com"
	// IrisBaseURL is the root of the web JSON:API; request paths keep their
	// /v1 or /v2 prefix.
	IrisBaseURL = ASCOrigin + "/iris"
	// OlympusSessionURL returns (GET) or switches (POST) the session's team.
	OlympusSessionURL = ASCOrigin + "/olympus/v1/session"
)

// ErrNoSession means no session is stored for the profile.
var ErrNoSession = errors.New("no App Store Connect web session stored")

// ErrExpired means Apple rejected the stored session (sign in again).
var ErrExpired = errors.New("the App Store Connect web session expired")

// Cookie is one stored cookie. Values are secrets: never log or return them.
type Cookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"` // host or ".apple.com" style suffix
	Path     string    `json:"path,omitempty"`
	Expires  time.Time `json:"expires,omitzero"` // zero = session cookie
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"http_only,omitempty"`
}

// Session is a signed-in web session for one Apple ID and team.
type Session struct {
	AppleID      string    `json:"apple_id"`
	ProviderID   int64     `json:"provider_id,omitempty"`
	ProviderName string    `json:"provider_name,omitempty"`
	Cookies      []Cookie  `json:"cookies"`
	CreatedAt    time.Time `json:"created_at"`
	// Source is how the session was made: "login" or "import:<format>".
	Source string `json:"source,omitempty"`
}

// String never includes cookie values.
func (s *Session) String() string {
	if s == nil {
		return "web session <nil>"
	}
	return fmt.Sprintf("web session %s team %d (%d cookies)", s.AppleID, s.ProviderID, len(s.Cookies))
}

// GoString matches String so %#v cannot leak cookie values.
func (s *Session) GoString() string { return s.String() }

// CookieHeader returns the Cookie header value for an https request to host
// and path at now: unexpired cookies whose domain matches host. "" when none.
func (s *Session) CookieHeader(host, path string, now time.Time) string {
	if s == nil {
		return ""
	}
	host = strings.ToLower(host)
	var parts []string
	for _, c := range s.Cookies {
		if c.Name == "" || (!c.Expires.IsZero() && !c.Expires.After(now)) {
			continue
		}
		if !domainMatch(host, c.Domain) || !pathMatch(path, c.Path) {
			continue
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// domainMatch implements RFC 6265 domain matching; a leading dot is ignored.
func domainMatch(host, domain string) bool {
	d := strings.ToLower(strings.TrimPrefix(domain, "."))
	return d != "" && (host == d || strings.HasSuffix(host, "."+d))
}

// pathMatch implements RFC 6265 path matching; an empty cookie path is "/".
func pathMatch(reqPath, cookiePath string) bool {
	if cookiePath == "" || cookiePath == "/" {
		return true
	}
	if reqPath == "" {
		reqPath = "/"
	}
	return reqPath == cookiePath ||
		(strings.HasPrefix(reqPath, cookiePath) &&
			(strings.HasSuffix(cookiePath, "/") || reqPath[len(cookiePath)] == '/'))
}

// Store persists one session per credentials profile. The default
// implementation (store.go) uses the OS keychain and falls back to a 0600
// file in the state directory.
type Store interface {
	// Load returns ErrNoSession when nothing is stored for profile.
	Load(profile string) (*Session, error)
	// Save replaces the stored session for profile.
	Save(profile string, s *Session) error
	// Delete removes the session; deleting a missing session is not an error.
	Delete(profile string) error
}

// Provider is one App Store Connect team (content provider).
type Provider struct {
	ID       int64  `json:"provider_id"`
	PublicID string `json:"public_provider_id,omitempty"`
	Name     string `json:"name"`
}

// Info is what Apple reports for a live session.
type Info struct {
	AppleID   string     `json:"apple_id"`
	Provider  Provider   `json:"provider"`
	Available []Provider `json:"available_providers,omitempty"`
}

// olympusSession is the GET /olympus/v1/session response.
type olympusSession struct {
	User struct {
		EmailAddress string `json:"emailAddress"`
	} `json:"user"`
	Provider struct {
		ProviderID       int64  `json:"providerId"`
		PublicProviderID string `json:"publicProviderId"`
		Name             string `json:"name"`
	} `json:"provider"`
	AvailableProviders []struct {
		ProviderID       int64  `json:"providerId"`
		PublicProviderID string `json:"publicProviderId"`
		Name             string `json:"name"`
	} `json:"availableProviders"`
}

// Validate asks Apple whether s is live (GET /olympus/v1/session with its
// cookies) and returns the account and team. 401/403 return ErrExpired.
// hc nil uses http.DefaultClient; it must not follow redirects to other hosts
// with the cookies (http.Client never forwards a manual Cookie header across
// hosts).
func Validate(ctx context.Context, hc *http.Client, s *Session, now time.Time) (*Info, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OlympusSessionURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "olympus-ui")
	if h := s.CookieHeader(req.URL.Host, req.URL.Path, now); h != "" {
		req.Header.Set("Cookie", h)
	} else {
		return nil, ErrExpired
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("websession: validate: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("websession: validate: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrExpired
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("websession: validate: Apple returned HTTP %d", resp.StatusCode)
	}
	var o olympusSession
	if err := json.Unmarshal(body, &o); err != nil {
		return nil, fmt.Errorf("websession: validate: decode session: %w", err)
	}
	if o.Provider.ProviderID == 0 && o.User.EmailAddress == "" {
		return nil, ErrExpired
	}
	info := &Info{
		AppleID:  o.User.EmailAddress,
		Provider: Provider{ID: o.Provider.ProviderID, PublicID: o.Provider.PublicProviderID, Name: o.Provider.Name},
	}
	for _, p := range o.AvailableProviders {
		info.Available = append(info.Available, Provider{ID: p.ProviderID, PublicID: p.PublicProviderID, Name: p.Name})
	}
	return info, nil
}

// TwoFactor describes a pending two-factor challenge shown to the human.
type TwoFactor struct {
	// Kind is "trusted_device" (code pushed to the user's devices) or "sms".
	Kind string
	// Phones are the masked trusted numbers ("•••• 12"), in Apple's order.
	Phones []string
	// CodeLength is the expected number of digits (usually 6).
	CodeLength int
	// Rejected is true when Apple refused the previous code for this
	// challenge; the human gets up to three tries.
	Rejected bool
}

// LoginOptions configures [Login] (login.go).
type LoginOptions struct {
	AppleID  string
	Password string
	// HTTP must have a cookie jar; nil builds one with a 30 s timeout.
	HTTP *http.Client
	// Code is called when Apple asks for a two-factor code. Returning
	// "sms:<n>" (1-based phone index) asks Apple to text that phone instead;
	// Code is then called again with Kind "sms".
	Code func(ctx context.Context, c TwoFactor) (string, error)
	// Now defaults to time.Now.
	Now func() time.Time
}

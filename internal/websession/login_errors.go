package websession

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// LoginErrorKind classifies why [Login] failed.
type LoginErrorKind string

// Login failure kinds.
const (
	// LoginWrongPassword: Apple rejected the Apple ID or password (-20101).
	LoginWrongPassword LoginErrorKind = "wrong_password"
	// LoginAccountLocked: Apple locked the account for this sign-in (-20209).
	LoginAccountLocked LoginErrorKind = "account_locked"
	// LoginActionRequired: the account needs attention in a browser first.
	LoginActionRequired LoginErrorKind = "action_required"
	// LoginCodeRejected: Apple rejected the two-factor code.
	LoginCodeRejected LoginErrorKind = "code_rejected"
	// LoginRateLimited: Apple is throttling sign-in (429/503).
	LoginRateLimited LoginErrorKind = "rate_limited"
	// LoginTwoFactorUnavailable: Apple wants a code but none can be obtained
	// (no prompt, or no trusted phone number).
	LoginTwoFactorUnavailable LoginErrorKind = "two_factor_unavailable"
	// LoginNoASCAccess: Apple signed in but App Store Connect refused it.
	LoginNoASCAccess LoginErrorKind = "no_asc_access"
	// LoginUnexpected: any other status from Apple.
	LoginUnexpected LoginErrorKind = "unexpected_status"
)

// LoginError is a human-readable sign-in failure. It never carries the
// password, codes, SRP values, cookies or session headers: only the stage,
// HTTP status, Apple's numeric service-error codes and Retry-After.
type LoginError struct {
	Kind LoginErrorKind
	// Stage is the step that failed, e.g. "signin_complete".
	Stage  string
	Status int
	// Codes are Apple's numeric serviceErrors codes ("-20101").
	Codes []string
	// RetryAfter is Apple's Retry-After value when it was numeric.
	RetryAfter string
}

// Error implements error.
func (e *LoginError) Error() string {
	var msg string
	switch e.Kind {
	case LoginWrongPassword:
		msg = "Apple rejected the Apple ID or password; check them and try again"
	case LoginAccountLocked:
		msg = "Apple has locked this Apple ID for password sign-in from tools (a browser sign-in still works); do not retry, it cannot clear the lock. Instead sign in at https://appstoreconnect.apple.com in a browser and run `app-store-connect-mcp web import` with its cookies (see `web import --help`), or unlock the Apple ID at https://iforgot.apple.com"
	case LoginActionRequired:
		msg = "this Apple ID needs attention before it can sign in (for example accepting new terms or upgrading security); sign in at https://appstoreconnect.apple.com in a browser, finish what Apple asks, then retry"
	case LoginCodeRejected:
		msg = "Apple rejected the two-factor code; run the login again and enter the newest code"
	case LoginRateLimited:
		msg = "Apple is rate-limiting sign-in attempts; wait"
		if e.RetryAfter != "" {
			msg += " " + e.RetryAfter + " seconds"
		} else {
			msg += " a few minutes (sometimes up to an hour)"
		}
		msg += " before trying again, and avoid repeated attempts"
	case LoginTwoFactorUnavailable:
		msg = "Apple asked for a two-factor code but none could be obtained (no interactive prompt, or no trusted device or phone number)"
	case LoginNoASCAccess:
		msg = "Apple signed in, but App Store Connect did not accept the session; make sure this Apple ID belongs to an App Store Connect team"
	default:
		msg = "Apple's sign-in service returned an unexpected response"
	}
	var detail []string
	if e.Stage != "" {
		detail = append(detail, "stage "+e.Stage)
	}
	if e.Status != 0 {
		detail = append(detail, "HTTP "+strconv.Itoa(e.Status))
	}
	if len(e.Codes) > 0 {
		detail = append(detail, "Apple codes "+strings.Join(e.Codes, ","))
	}
	if len(detail) > 0 {
		msg += " (" + strings.Join(detail, ", ") + ")"
	}
	return "websession: login: " + msg
}

// newLoginError classifies a non-success idmsa response. Only numeric
// service-error codes and a numeric Retry-After are kept.
func newLoginError(stage string, resp *http.Response, body []byte) *LoginError {
	e := &LoginError{Kind: LoginUnexpected, Stage: stage, Status: resp.StatusCode, Codes: loginServiceCodes(body)}
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if _, err := strconv.Atoi(ra); err == nil && len(ra) <= 6 {
			e.RetryAfter = ra
		}
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
		e.Kind = LoginRateLimited
	case stage == "signin_complete" && resp.StatusCode == http.StatusUnauthorized &&
		(len(e.Codes) == 0 || e.hasCode("-20101")):
		e.Kind = LoginWrongPassword
	case e.hasCode("-20209"):
		e.Kind = LoginAccountLocked
	case stage == "signin_complete" && resp.StatusCode == http.StatusPreconditionFailed:
		e.Kind = LoginActionRequired
	case strings.HasPrefix(stage, "verify_") && resp.StatusCode >= 400 && resp.StatusCode < 500:
		e.Kind = LoginCodeRejected
	}
	return e
}

func (e *LoginError) hasCode(code string) bool {
	for _, c := range e.Codes {
		if c == code {
			return true
		}
	}
	return false
}

// loginServiceCodes extracts Apple's numeric serviceErrors codes (at most 5).
func loginServiceCodes(body []byte) []string {
	var payload struct {
		ServiceErrors []struct {
			Code string `json:"code"`
		} `json:"serviceErrors"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	var codes []string
	for _, se := range payload.ServiceErrors {
		c := strings.TrimSpace(se.Code)
		if len(codes) == 5 || c == "" || len(c) > 12 {
			continue
		}
		if _, err := strconv.Atoi(c); err != nil {
			continue
		}
		codes = append(codes, c)
	}
	return codes
}

// errLoginf builds a non-HTTP login error with fixed text.
func errLoginf(format string, args ...any) error {
	return fmt.Errorf("websession: login: "+format, args...)
}

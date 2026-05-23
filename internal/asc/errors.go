package asc

import (
	"net/http"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// Error hints keyed by situation.
const (
	hint401 = "authentication failed: run doctor to check the key id, issuer id, key kind and clock skew"
	hint403 = "the API key lacks permission: check its role in Users and Access › Integrations, " +
		"or whether this endpoint needs a Team key instead of an Individual key"
	hint404 = "not found: check the id belongs to the resource type in the path (e.g. an app id vs. " +
		"a version id) and that the key can see it"
	hintState = "the object is in a state that forbids this change: read it back to see its current " +
		"state before retrying"
	hint429 = "hourly App Store Connect quota exhausted (3600 calls/h): wait, or batch with include= " +
		"and fields[]"
	hint5xx      = "Apple server error: check https://developer.apple.com/system-status/ and retry later"
	maxErrDetail = 500
)

// apiError builds the redacted, hinted *tool.APIError for a non-2xx response
// to method+path, attaching a doctor next step on 401.
func (c *Client) apiError(status int, body []byte, method, path string) error {
	var ae *tool.APIError
	if doc, err := tool.ParseDocument(body); err == nil {
		ae = doc.Err(status)
	}
	if ae == nil {
		ae = &tool.APIError{Status: status, Title: http.StatusText(status)}
		if s := strings.TrimSpace(string(body)); s != "" {
			ae.Detail = truncate(s, maxErrDetail)
		}
	}
	redactAPIError(ae)
	ae.Hint = c.hint(ae, method, path)
	if status == http.StatusUnauthorized {
		return tool.WithNext(ae, tool.NextStep{Tool: "doctor"})
	}
	return ae
}

// redactAPIError strips credentials from every text field of e and e.More.
func redactAPIError(e *tool.APIError) {
	e.Title, e.Detail, e.Pointer = Redact(e.Title), Redact(e.Detail), Redact(e.Pointer)
	for i := range e.More {
		redactAPIError(&e.More[i])
	}
}

// hint maps an API error to advice for the agent.
func (c *Client) hint(e *tool.APIError, method, path string) string {
	switch {
	case e.Status == http.StatusUnauthorized:
		return hint401
	case e.Status == http.StatusForbidden:
		return hint403
	case e.Status == http.StatusNotFound:
		return hint404
	case e.Status == http.StatusTooManyRequests:
		return hint429
	case e.Status >= http.StatusInternalServerError:
		return hint5xx
	}
	if h := c.enumHint(e, method, path); h != "" {
		return h
	}
	if e.Status == http.StatusConflict && strings.HasPrefix(e.Code, "STATE_ERROR") {
		return hintState
	}
	return ""
}

// enumHint lists the valid values at the offending pointer of an invalid
// attribute or parameter error, using the spec.
func (c *Client) enumHint(e *tool.APIError, method, path string) string {
	if c.spec == nil || e.Pointer == "" {
		return ""
	}
	invalid := strings.HasPrefix(e.Code, "ENTITY_ERROR.ATTRIBUTE.INVALID") ||
		strings.HasPrefix(e.Code, "PARAMETER_ERROR.INVALID")
	if !invalid && e.Status != http.StatusUnprocessableEntity {
		return ""
	}
	path, _, _ = strings.Cut(path, "?")
	op, ok := c.spec.Match(method, path)
	if !ok {
		return ""
	}
	vals := c.spec.EnumLookup(op.ID, e.Pointer)
	if len(vals) == 0 {
		return ""
	}
	return "invalid value at " + e.Pointer + "; valid values: " + strings.Join(vals, ", ")
}

// truncate shortens s to at most n bytes on a rune boundary, adding "…".
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// isRuneStart reports whether b starts a UTF-8 sequence.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

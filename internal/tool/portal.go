package tool

import (
	"context"
	"fmt"
	"strings"
)

// Portal reaches App Store Connect's web-only JSON endpoints with the
// human's web session (the same Apple ID cookies as [Env.Web]): the App
// Analytics dashboard API (/analytics/api/v1/...), Payments and Financial
// Reports (/WebObjects/iTunesConnect.woa/ra/paymentConsolidation/...) and
// /olympus/v1/session. Every call is a read, POST included: dashboard
// queries are POSTs that change nothing.
type Portal interface {
	// JSON sends method to path (one of the allowed prefixes above, query
	// allowed) with body encoded as JSON when non-nil, and decodes a 2xx
	// JSON answer into out (out may be nil). A non-2xx JSON answer returns
	// *PortalError. No usable session (none stored, expired after one
	// renewal attempt, or an HTML/redirect answer) returns an error
	// wrapping [ErrWebSessionRequired].
	JSON(ctx context.Context, method, path string, body, out any) error
}

// PortalError is a non-2xx answer of a [Portal] endpoint. Messages are
// Apple's error texts (errors[].message), untrusted.
type PortalError struct {
	Status   int
	Messages []string
}

func (e *PortalError) Error() string {
	if len(e.Messages) == 0 {
		return fmt.Sprintf("App Store Connect web %d", e.Status)
	}
	return fmt.Sprintf("App Store Connect web %d: %s", e.Status, SanitizeText(strings.Join(e.Messages, "; ")))
}

type noPortal struct{}

func (noPortal) JSON(context.Context, string, string, any, any) error {
	return ErrWebSessionRequired
}

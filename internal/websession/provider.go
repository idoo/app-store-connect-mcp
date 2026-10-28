// Protocol details follow rudrankriyam/App-Store-Connect-CLI (MIT).

package websession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SelectProvider switches s to the App Store Connect team providerID
// (POST /olympus/v1/session) and keeps the cookies Apple rotates. It sets
// s.ProviderID; callers refresh the team name with [Validate]. hc nil uses
// http.DefaultClient. 401/403 return ErrExpired.
func SelectProvider(ctx context.Context, hc *http.Client, s *Session, providerID int64, now time.Time) error {
	if s == nil {
		return ErrNoSession
	}
	if providerID <= 0 {
		return errors.New("websession: select team: invalid team id")
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	body, err := json.Marshal(map[string]map[string]int64{"provider": {"providerId": providerID}})
	if err != nil {
		return fmt.Errorf("websession: select team: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OlympusSessionURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("websession: select team: %w", err)
	}
	h := s.CookieHeader(req.URL.Hostname(), req.URL.Path, now)
	if h == "" {
		return ErrExpired
	}
	req.Header.Set("Cookie", h)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "olympus-ui")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("websession: select team: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	mergeCookies(s, resp.Cookies(), req.URL.Hostname(), now)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrExpired
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("websession: select team %d: Apple returned HTTP %d", providerID, resp.StatusCode)
	}
	s.ProviderID = providerID
	s.ProviderName = ""
	return nil
}

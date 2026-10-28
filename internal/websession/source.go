package websession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// saveThrottle bounds how often rotated non-critical cookies are persisted.
const saveThrottle = 30 * time.Second

// renewBackoff is how long Authorize waits after a failed renewal before
// trying again.
const renewBackoff = 15 * time.Minute

// Source authorizes web (iris) requests with the stored session of one
// profile. It satisfies asc.SessionAuth and is safe for concurrent use.
type Source struct {
	profile string
	store   Store
	now     func() time.Time

	mu       sync.Mutex
	sess     *Session
	dead     string // fingerprint of the session Apple last rejected
	lastSave time.Time

	renew     func(ctx context.Context) (*Session, error)
	renewing  chan struct{} // non-nil while a renewal runs; closed when it ends
	renewFail time.Time     // when the last renewal failed; zero after success
	renewErr  error
}

// NewSource returns a Source for profile backed by store; now nil means
// time.Now.
func NewSource(profile string, store Store, now func() time.Time) *Source {
	if now == nil {
		now = time.Now
	}
	return &Source{profile: profile, store: store, now: now}
}

// SetRenewer installs fn, called when Authorize finds no usable session (none stored, or
// Apple rejected it via Expired). At most one renewal runs at a time; after a failure the
// next attempt waits 15 minutes. A returned session is saved to the store and used for the
// request. nil disables renewal. fn runs WITHOUT holding the Source mutex.
func (s *Source) SetRenewer(fn func(ctx context.Context) (*Session, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renew = fn
}

// Authorize adds the session's Cookie header and the headers App Store
// Connect's web UI sends. Without a usable session it runs the renewer (see
// SetRenewer) or returns an error wrapping tool.ErrWebSessionRequired.
func (s *Source) Authorize(ctx context.Context, req *http.Request) error {
	waited := false
	for {
		s.mu.Lock()
		h, renewable, err := s.headerLocked(req)
		if err == nil {
			s.mu.Unlock()
			req.Header.Set("Cookie", h)
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			req.Header.Set("X-CSRF-ITC", "[asc-ui]")
			return nil
		}
		if !renewable || s.renew == nil {
			s.mu.Unlock()
			return err
		}
		if ch := s.renewing; ch != nil {
			s.mu.Unlock()
			select {
			case <-ch:
				waited = true
				continue
			case <-ctx.Done():
				return fmt.Errorf("%w: waiting for session renewal: %w", err, ctx.Err())
			}
		}
		if !s.renewFail.IsZero() && s.now().Sub(s.renewFail) < renewBackoff {
			rerr := s.renewErr
			s.mu.Unlock()
			return fmt.Errorf("%w (automatic renewal failed recently: %w)", err, rerr)
		}
		if waited {
			// Another call's renewal finished and the session is still unusable.
			s.mu.Unlock()
			return err
		}
		if rerr := s.renewOnce(ctx); rerr != nil {
			return fmt.Errorf("%w (automatic renewal failed: %w)", err, rerr)
		}
		waited = true
	}
}

// headerLocked returns the Cookie header for req from the cached or stored
// session. renewable reports that the failure is a missing or rejected
// session a renewal can fix. Callers hold s.mu.
func (s *Source) headerLocked(req *http.Request) (h string, renewable bool, err error) {
	if s.sess == nil {
		sess, err := s.store.Load(s.profile)
		switch {
		case errors.Is(err, ErrNoSession):
			return "", true, fmt.Errorf("%w: %w", tool.ErrWebSessionRequired, ErrNoSession)
		case err != nil:
			return "", false, fmt.Errorf("%w: %w", tool.ErrWebSessionRequired, err)
		}
		if s.dead != "" && fingerprint(sess) == s.dead {
			return "", true, fmt.Errorf("%w: session expired: %w", tool.ErrWebSessionRequired, ErrExpired)
		}
		s.sess, s.dead = sess, ""
	}
	h = s.sess.CookieHeader(req.URL.Hostname(), req.URL.Path, s.now())
	if h == "" {
		return "", true, fmt.Errorf("%w: session expired: %w", tool.ErrWebSessionRequired, ErrExpired)
	}
	return h, false, nil
}

// renewOnce runs the renewer without s.mu; s.mu is held on entry and
// released on return. Concurrent Authorize calls wait on s.renewing. A
// cancelled ctx does not start the backoff.
func (s *Source) renewOnce(ctx context.Context) error {
	ch := make(chan struct{})
	s.renewing = ch
	fn := s.renew
	s.mu.Unlock()

	sess, err := fn(ctx)
	if err == nil && sess == nil {
		err = errors.New("renewer returned no session")
	}
	var saveErr error
	if err == nil {
		saveErr = s.store.Save(s.profile, sess)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewing = nil
	close(ch)
	if err != nil {
		if ctx.Err() == nil {
			s.renewFail, s.renewErr = s.now(), err
		}
		return err
	}
	s.sess, s.dead = sess, ""
	s.renewFail, s.renewErr = time.Time{}, nil
	if saveErr == nil {
		// A failed save is retried by Observe's next critical change; the
		// session still serves this process.
		s.lastSave = s.now()
	}
	return nil
}

// Observe keeps cookies Apple rotates (Set-Cookie on *.apple.com
// responses). Changes are saved at most every 30 s unless myacinfo or
// dqsid changed.
func (s *Source) Observe(resp *http.Response) {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return
	}
	host := strings.ToLower(resp.Request.URL.Hostname())
	if !isAppleDomain(host) {
		return
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sess == nil {
		return
	}
	now := s.now()
	changed, critical := mergeCookies(s.sess, cookies, host, now)
	if !changed || (!critical && now.Sub(s.lastSave) < saveThrottle) {
		return
	}
	cp := *s.sess
	cp.Cookies = append([]Cookie(nil), s.sess.Cookies...)
	if err := s.store.Save(s.profile, &cp); err == nil {
		s.lastSave = now
	}
}

// Expired drops the cached session after Apple rejected it. The next
// Authorize reloads; if the store still holds the same session it fails.
func (s *Source) Expired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sess != nil {
		s.dead = fingerprint(s.sess)
	}
	s.sess = nil
}

// fingerprint identifies a session by its cookies without keeping values.
func fingerprint(sess *Session) string {
	parts := make([]string, 0, len(sess.Cookies))
	for _, c := range sess.Cookies {
		parts = append(parts, c.Name+"\x00"+normDomain(c.Domain)+"\x00"+c.Path+"\x00"+c.Value)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(sum[:])
}

func normDomain(d string) string { return strings.ToLower(strings.TrimPrefix(d, ".")) }

// isAppleDomain reports whether d is apple.com or a subdomain.
func isAppleDomain(d string) bool {
	d = normDomain(d)
	return d == "apple.com" || strings.HasSuffix(d, ".apple.com")
}

// mergeCookies applies Set-Cookie values received from host to s. It reports
// whether anything changed and whether myacinfo or dqsid did.
func mergeCookies(s *Session, cookies []*http.Cookie, host string, now time.Time) (changed, critical bool) {
	for _, hc := range cookies {
		if hc == nil || hc.Name == "" {
			continue
		}
		domain := hc.Domain
		if domain == "" {
			domain = host
		}
		if !isAppleDomain(domain) {
			continue
		}
		c := Cookie{Name: hc.Name, Value: hc.Value, Domain: domain, Path: hc.Path, Secure: hc.Secure, HTTPOnly: hc.HttpOnly}
		remove := false
		switch {
		case hc.MaxAge < 0:
			remove = true
		case hc.MaxAge > 0:
			c.Expires = now.Add(time.Duration(hc.MaxAge) * time.Second)
		case !hc.Expires.IsZero():
			c.Expires = hc.Expires
			remove = !hc.Expires.After(now)
		}
		idx := -1
		for i, old := range s.Cookies {
			if old.Name == c.Name && normDomain(old.Domain) == normDomain(c.Domain) && cookiePath(old.Path) == cookiePath(c.Path) {
				idx = i
				break
			}
		}
		did := false
		switch {
		case remove && idx >= 0:
			s.Cookies = append(s.Cookies[:idx], s.Cookies[idx+1:]...)
			did = true
		case remove:
		case idx >= 0:
			if s.Cookies[idx] != c {
				did = s.Cookies[idx].Value != c.Value || !s.Cookies[idx].Expires.Equal(c.Expires)
				s.Cookies[idx] = c
			}
		default:
			s.Cookies = append(s.Cookies, c)
			did = true
		}
		if did {
			changed = true
			if c.Name == "myacinfo" || c.Name == "dqsid" {
				critical = true
			}
		}
	}
	return changed, critical
}

func cookiePath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

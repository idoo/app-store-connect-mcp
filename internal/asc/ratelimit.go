package asc

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

// RateLimitHeader is Apple's quota header, e.g.
// "user-hour-lim:3600;user-hour-rem:3597".
const RateLimitHeader = "X-Rate-Limit"

// rateFresh is how long a quota reading outranks higher ones: the hourly
// window can legitimately refill, so after this a higher reading is accepted.
const rateFresh = time.Minute

// maxAuthSkew is the clock skew beyond which a 401 is blamed on the local
// clock and not retried with a fresh token.
const maxAuthSkew = time.Minute

// rateObs is one quota reading and when it was observed.
type rateObs struct {
	rl tool.RateLimit
	at time.Time
}

// parseRateLimit parses an X-Rate-Limit header value. It reports false when
// neither the limit nor the remaining count is present.
func parseRateLimit(v string) (tool.RateLimit, bool) {
	var rl tool.RateLimit
	var haveLim, haveRem bool
	for part := range strings.SplitSeq(v, ";") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || n < 0 {
			continue
		}
		switch strings.TrimSpace(k) {
		case "user-hour-lim":
			rl.Limit, haveLim = n, true
		case "user-hour-rem":
			rl.Remaining, haveRem = n, true
		}
	}
	if !haveLim && !haveRem {
		return tool.RateLimit{}, false
	}
	rl.Known = true
	return rl, true
}

// observe records the rate limit and clock skew carried by h. Responses of
// concurrent requests arrive out of order, so a reading only replaces the
// current one when it is lower or the current one is older than [rateFresh].
func (c *Client) observe(h http.Header) {
	if rl, ok := parseRateLimit(h.Get(RateLimitHeader)); ok {
		obs := &rateObs{rl: rl, at: c.clock.Now()}
		for {
			cur := c.rate.Load()
			if cur != nil && rl.Remaining > cur.rl.Remaining && obs.at.Sub(cur.at) < rateFresh {
				break
			}
			if c.rate.CompareAndSwap(cur, obs) {
				break
			}
		}
	}
	if d := h.Get("Date"); d != "" {
		if t, err := http.ParseTime(d); err == nil {
			c.skew.Store(int64(c.clock.Now().Sub(t)))
			c.skewKnown.Store(true)
		}
	}
}

// RateLimit implements [tool.ASC]: the lowest recent quota reading.
func (c *Client) RateLimit() tool.RateLimit {
	if obs := c.rate.Load(); obs != nil {
		return obs.rl
	}
	return tool.RateLimit{}
}

// quotaExhausted fails fast with tool.ErrRateLimited when Apple reported no
// calls left within the last [rateFresh], instead of spending retries on 429s.
func (c *Client) quotaExhausted(label string) error {
	obs := c.rate.Load()
	if obs == nil || obs.rl.Remaining > 0 || c.clock.Now().Sub(obs.at) >= rateFresh {
		return nil
	}
	return fmt.Errorf("asc: %s: hourly quota of %d calls used up: %w", label, obs.rl.Limit, tool.ErrRateLimited)
}

// ClockSkew implements [tool.ASC]: local time minus Apple's Date header of
// the latest response (1 s resolution).
func (c *Client) ClockSkew() (time.Duration, bool) {
	if !c.skewKnown.Load() {
		return 0, false
	}
	return time.Duration(c.skew.Load()), true
}

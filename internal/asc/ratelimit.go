package asc

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

// RateLimitHeader is Apple's quota header, e.g.
// "user-hour-lim:3600;user-hour-rem:3597".
const RateLimitHeader = "X-Rate-Limit"

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

// observe records the rate limit and clock skew carried by h.
func (c *Client) observe(h http.Header) {
	if rl, ok := parseRateLimit(h.Get(RateLimitHeader)); ok {
		c.rate.Store(&rl)
	}
	if d := h.Get("Date"); d != "" {
		if t, err := http.ParseTime(d); err == nil {
			c.skew.Store(int64(c.clock.Now().Sub(t)))
			c.skewKnown.Store(true)
		}
	}
}

// RateLimit implements [tool.ASC]: the quota of the latest response.
func (c *Client) RateLimit() tool.RateLimit {
	if rl := c.rate.Load(); rl != nil {
		return *rl
	}
	return tool.RateLimit{}
}

// ClockSkew implements [tool.ASC]: local time minus Apple's Date header of
// the latest response (1 s resolution).
func (c *Client) ClockSkew() (time.Duration, bool) {
	if !c.skewKnown.Load() {
		return 0, false
	}
	return time.Duration(c.skew.Load()), true
}

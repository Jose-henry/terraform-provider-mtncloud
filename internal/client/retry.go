package client

import (
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// retryable reports whether a response status is worth another attempt. Only throttling and
// server-side failures are. A 4xx validation error never is: sending it again can't succeed.
func retryable(status int, idempotent bool) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true // the request was rejected before it was processed
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return idempotent // a write may have been applied, so only reads are repeated
	}
	return false
}

// backoff returns the wait before attempt n (n >= 1): exponential from base, capped, with full
// jitter so 50 parallel callers don't retry in lockstep. A Retry-After header wins when present.
func backoff(n int, base, maxWait time.Duration, retryAfter string) time.Duration {
	if s, err := strconv.Atoi(retryAfter); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, maxWait)
	}
	d := base << min(n-1, 16)
	if d <= 0 || d > maxWait {
		d = maxWait
	}
	return time.Duration(rand.Int64N(int64(d)) + 1)
}

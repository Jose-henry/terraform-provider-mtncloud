package client

import (
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
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

// labelRace reports a 400 from two requests creating the same new label at once: the platform adds
// a label row per tenant, and the second insert breaks its unique key ("Duplicate entry '72-tf-acc'
// for key 'label.UK…'", QA 2026-11-03, a Kubernetes and a Docker cluster created together). Only a
// cluster create retries it, after checking no cluster was made (CreateCluster).
func labelRace(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.StatusCode == http.StatusBadRequest &&
		strings.Contains(api.Message, "Duplicate entry") && strings.Contains(api.Message, "for key 'label.")
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

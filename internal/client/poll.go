package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// poll calls read until settled reports that the object reached a terminal state. Waits grow from
// Config.PollMin to Config.PollMax. It stops when ctx ends, so a resource's timeouts {} bound it:
// the error is then ctx's. Every exit returns the last value read without an error, so the
// caller can name the status the object was stuck in.
//
// settled sees every read, errors included, so each caller decides what a 404 means: still being
// created (rule 13), or gone at last after a delete. A read the platform didn't answer at all
// (unreachable) doesn't end a wait that has a deadline: it reads again, until the object settles or
// ctx ends, and a wait that ends during the outage returns the platform's error, not a timeout (the
// object's state is unknown, not stuck). QA cluster 146 (2026-11-03): an outage near the end of a
// 30-minute build stopped the wait, and the apply reported success for a cluster that then failed. A
// wait without a deadline still stops at the first such error: it would otherwise never end.
func poll[T any](ctx context.Context, c *Client, read func(context.Context) (T, error), settled func(T, error) (bool, error)) (T, error) {
	var last T
	var outage error // the last read the platform didn't answer, while that lasts
	_, bounded := ctx.Deadline()
	wait := c.cfg.PollMin
	for {
		v, err := read(ctx)
		if ctx.Err() != nil {
			if outage != nil {
				return last, outage
			}
			return last, ctx.Err()
		}
		if err != nil && bounded && unreachable(err) {
			// The rate limiter's "would pass the deadline" is the wait running out, not the platform.
			if !errors.Is(err, context.DeadlineExceeded) {
				outage = err
			}
			c.log(ctx, "MTN Cloud didn't answer while waiting; reading again", map[string]any{"error": err.Error()})
			if serr := sleep(ctx, wait); serr != nil {
				if outage != nil {
					return last, outage
				}
				return last, serr
			}
			wait = min(wait*2, c.cfg.PollMax)
			continue
		}
		outage = nil
		if err == nil {
			last = v
		}
		done, err := settled(v, err)
		if done || err != nil {
			if err == nil {
				return v, nil
			}
			return last, err
		}
		if err := sleep(ctx, wait); err != nil {
			return last, err
		}
		wait = min(wait*2, c.cfg.PollMax)
	}
}

// unreachable reports an error where the platform didn't answer: a dropped or refused connection, a
// connect timeout, or a gateway in front of it failing. The object's state is unknown, not bad.
func unreachable(err error) bool {
	if isTransient(err) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	var api *APIError
	if errors.As(err, &api) {
		switch api.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
	}
	return false
}

// pollDefaults fills the polling fields of a Config.
func pollDefaults(cfg *Config) {
	if cfg.PollMin <= 0 {
		cfg.PollMin = 500 * time.Millisecond
	}
	if cfg.PollMax <= 0 {
		cfg.PollMax = 15 * time.Second
	}
	if cfg.PowerGrace <= 0 {
		cfg.PowerGrace = 2 * time.Minute
	}
	if cfg.LBAttempt <= 0 {
		cfg.LBAttempt = 3 * time.Minute
	}
	if cfg.VersionInUseWait <= 0 {
		cfg.VersionInUseWait = 5 * time.Minute
	}
	if cfg.LBWriteAttempt <= 0 {
		cfg.LBWriteAttempt = 8 * time.Minute
	}
	if cfg.LBPendingGrace <= 0 {
		cfg.LBPendingGrace = 2 * time.Minute
	}
	if cfg.MOSLostAfter <= 0 {
		cfg.MOSLostAfter = 30 * time.Second
	}
}

// Await polls until ready reports true, with the client's backoff, until ctx ends. For a state the
// client can't read by itself, such as an order's VM found by the resource's own rules.
func (c *Client) Await(ctx context.Context, ready func(context.Context) (bool, error)) error {
	_, err := poll(ctx, c, func(ctx context.Context) (bool, error) { return ready(ctx) },
		func(ok bool, err error) (bool, error) { return ok, err })
	return err
}

package client

import (
	"context"
	"time"
)

// poll calls read until settled reports that the object reached a terminal state. Waits grow from
// Config.PollMin to Config.PollMax. It stops when ctx ends, so a resource's timeouts {} bound it:
// the error is then ctx's. Every exit returns the last value read without an error, so the
// caller can name the status the object was stuck in.
//
// settled sees every read, errors included, so each caller decides what a 404 means: still being
// created (rule 13), or gone at last after a delete.
func poll[T any](ctx context.Context, c *Client, read func(context.Context) (T, error), settled func(T, error) (bool, error)) (T, error) {
	var last T
	wait := c.cfg.PollMin
	for {
		v, err := read(ctx)
		if ctx.Err() != nil {
			return last, ctx.Err()
		}
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

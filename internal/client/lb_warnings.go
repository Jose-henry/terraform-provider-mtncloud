package client

import (
	"context"
	"sync"
)

// LBWarnings collects the warnings load balancer saves answer with. A save that succeeded can still
// carry one: an L7 rule in ERROR on a policy that was changed, possibly from before (plugin,
// 2026-11-03). The save is not failed for it; the caller shows it.
type LBWarnings struct {
	mu   sync.Mutex
	list []string
}

type lbWarningsKey struct{}

// WithLBWarnings returns a context whose load balancer saves add their warnings to w.
func WithLBWarnings(ctx context.Context) (context.Context, *LBWarnings) {
	w := &LBWarnings{}
	return context.WithValue(ctx, lbWarningsKey{}, w), w
}

// List returns the warnings collected so far.
func (w *LBWarnings) List() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.list...)
}

// AddLBWarning adds msg to the context's collector, and reports whether there is one.
func AddLBWarning(ctx context.Context, msg string) bool {
	w, ok := ctx.Value(lbWarningsKey{}).(*LBWarnings)
	if !ok {
		return false
	}
	w.mu.Lock()
	w.list = append(w.list, msg)
	w.mu.Unlock()
	return true
}

// lbWarn hands a save's warning to the context's collector, or logs it when there is none.
func (c *Client) lbWarn(ctx context.Context, action, msg string) {
	msg = lbCustomerText(msg)
	if !AddLBWarning(ctx, msg) {
		c.log(ctx, "load balancer save answered with a warning", map[string]any{"action": action, "warning": msg})
	}
}

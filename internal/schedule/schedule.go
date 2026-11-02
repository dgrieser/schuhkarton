// Package schedule keeps background work inside a daily time window, like
// 23:00 to 05:00, so that it does not compete with people looking at photos
// or with the network during the day.
package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Window is a daily span of local time. From == To (the zero value) means
// any time. A window may pass midnight: From 23:00, To 05:00.
type Window struct {
	From, To int // minutes after midnight
}

// Parse reads "HH:MM" times; two empty strings mean any time.
func Parse(from, to string) (Window, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" && to == "" {
		return Window{}, nil
	}
	if from == "" || to == "" {
		return Window{}, fmt.Errorf("need a start and an end time")
	}
	f, err := clock(from)
	if err != nil {
		return Window{}, err
	}
	t, err := clock(to)
	if err != nil {
		return Window{}, err
	}
	if f == t {
		return Window{}, fmt.Errorf("start and end are the same; leave both empty for any time")
	}
	return Window{From: f, To: t}, nil
}

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is no time like 23:00", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Any tells whether work may run at any time.
func (w Window) Any() bool { return w.From == w.To }

func (w Window) String() string {
	if w.Any() {
		return ""
	}
	return fmt.Sprintf("%02d:%02d–%02d:%02d", w.From/60, w.From%60, w.To/60, w.To%60)
}

// FromText is the start time as "HH:MM".
func (w Window) FromText() string { return fmt.Sprintf("%02d:%02d", w.From/60, w.From%60) }

// Open tells whether work may run at t.
func (w Window) Open(t time.Time) bool {
	if w.Any() {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if w.From < w.To {
		return m >= w.From && m < w.To
	}
	return m >= w.From || m < w.To // over midnight
}

// at returns the day of t at minute m.
func at(t time.Time, m int) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d, m/60, m%60, 0, 0, t.Location())
}

// Next returns when the window opens next: t itself while it is open.
func (w Window) Next(t time.Time) time.Time {
	if w.Open(t) {
		return t
	}
	n := at(t, w.From)
	if !n.After(t) {
		n = at(t.AddDate(0, 0, 1), w.From)
	}
	return n
}

// End returns when the window that is open at t closes (the zero time for
// any time or a closed window).
func (w Window) End(t time.Time) time.Time {
	if w.Any() || !w.Open(t) {
		return time.Time{}
	}
	e := at(t, w.To)
	if !e.After(t) {
		e = at(t.AddDate(0, 0, 1), w.To)
	}
	return e
}

// poll is how often waiting work looks at the clock. Timers alone would
// oversleep a suspended laptop, so the wall clock decides.
var poll = 30 * time.Second

// Wait blocks until the window is open. The context it returns ends with
// the window (or with ctx); ok is false when ctx ended first.
func (w Window) Wait(ctx context.Context) (wctx context.Context, cancel context.CancelFunc, ok bool) {
	if w.Any() {
		wctx, cancel = context.WithCancel(ctx)
		return wctx, cancel, true
	}
	for !w.Open(time.Now()) {
		t := time.NewTimer(min(poll, time.Until(w.Next(time.Now()))+time.Second))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, nil, false
		case <-t.C:
		}
	}
	wctx, cancel = context.WithCancel(ctx)
	every := poll
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-t.C:
				if !w.Open(time.Now()) {
					cancel()
					return
				}
			}
		}
	}()
	return wctx, cancel, true
}

// Closed tells whether wctx ended because the window closed while the
// outer ctx goes on.
func Closed(ctx, wctx context.Context) bool { return wctx.Err() != nil && ctx.Err() == nil }

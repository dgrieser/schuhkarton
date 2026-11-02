package schedule

import (
	"context"
	"testing"
	"time"
)

func TestWindow(t *testing.T) {
	if w, err := Parse("7:30", "8:00"); err != nil || w.From != 450 {
		t.Errorf("one-digit hour: %v %v", w, err)
	}
	night, err := Parse("23:00", "05:00")
	if err != nil {
		t.Fatal(err)
	}
	day, _ := Parse("09:30", "17:00")
	day0 := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	h := func(hh, mm int) time.Time {
		return day0.Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
	}
	for _, c := range []struct {
		w    Window
		t    time.Time
		open bool
		next time.Time
		end  time.Time
	}{
		{night, h(22, 59), false, h(23, 0), time.Time{}},
		{night, h(23, 0), true, h(23, 0), h(29, 0)},
		{night, h(2, 0), true, h(2, 0), h(5, 0)},
		{night, h(5, 0), false, h(23, 0), time.Time{}},
		{day, h(9, 29), false, h(9, 30), time.Time{}},
		{day, h(12, 0), true, h(12, 0), h(17, 0)},
		{day, h(17, 0), false, h(33, 30), time.Time{}},
	} {
		if got := c.w.Open(c.t); got != c.open {
			t.Errorf("%s at %s: open %v", c.w, c.t.Format("15:04"), got)
		}
		if got := c.w.Next(c.t); !got.Equal(c.next) {
			t.Errorf("%s at %s: next %s, want %s", c.w, c.t.Format("15:04"), got, c.next)
		}
		if got := c.w.End(c.t); !got.Equal(c.end) {
			t.Errorf("%s at %s: end %s, want %s", c.w, c.t.Format("15:04"), got, c.end)
		}
	}
	any, _ := Parse("", "")
	if !any.Any() || !any.Open(h(3, 0)) || any.String() != "" {
		t.Error("empty is any time")
	}
	if night.String() != "23:00–05:00" || night.FromText() != "23:00" {
		t.Errorf("text %q", night.String())
	}
	for _, bad := range [][2]string{{"23:00", ""}, {"24:00", "05:00"}, {"abends", "05:00"}, {"23:60", "1:00"}, {"23:00", "23:00"}, {"23:00oops", "05:00"}} {
		if _, err := Parse(bad[0], bad[1]); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestWait(t *testing.T) {
	poll = 20 * time.Millisecond
	defer func() { poll = 30 * time.Second }()
	ctx := context.Background()
	// open now: returns at once, ends when the window closes
	now := time.Now()
	m := now.Hour()*60 + now.Minute()
	open := Window{From: (m + 1439) % 1440, To: (m + 1) % 1440}
	wctx, cancel, ok := open.Wait(ctx)
	if !ok || wctx.Err() != nil {
		t.Fatal("open window waits")
	}
	cancel()
	if !Closed(ctx, wctx) {
		t.Error("cancelled window context")
	}
	// closed now: waits until ctx ends
	closed := Window{From: (m + 120) % 1440, To: (m + 180) % 1440}
	c2, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if _, _, ok := closed.Wait(c2); ok {
		t.Error("closed window ran")
	}
}

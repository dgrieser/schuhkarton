package schedule

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWindow(t *testing.T) {
	if w, err := Parse("7:30", "8:00"); err != nil || w.Rules[0].From != 450 {
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
	if night.String() != "23:00–05:00" {
		t.Errorf("text %q", night.String())
	}
	for _, bad := range [][2]string{{"23:00", ""}, {"24:00", "05:00"}, {"abends", "05:00"}, {"23:60", "1:00"}, {"23:00", "23:00"}, {"23:00oops", "05:00"}} {
		if _, err := Parse(bad[0], bad[1]); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Mon–Fri 23:00–08:00, Sat–Sun 00:00–07:00, and a one-time Tuesday morning.
func TestWeekRules(t *testing.T) {
	week, _ := ParseRule("mon-fri", "23:00", "08:00")
	end, _ := ParseRule("sa,so", "00:00", "07:00")
	mon := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local) // a Monday
	h := func(day, hh, mm int) time.Time {
		return mon.AddDate(0, 0, day).Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
	}
	once, err := ParseSpan("2026-11-03 09:00", "13:00", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	w := Window{Rules: []Rule{*week, *end}, Extra: []Span{once}}
	for _, c := range []struct {
		t    time.Time
		open bool
		next time.Time
		end  time.Time
	}{
		{h(0, 3, 0), false, h(0, 23, 0), time.Time{}}, // Monday night: Sunday has no rule past 07:00
		{h(0, 23, 0), true, h(0, 23, 0), h(1, 8, 0)},
		{h(1, 7, 59), true, h(1, 7, 59), h(1, 8, 0)},
		{h(1, 8, 0), false, h(1, 9, 0), time.Time{}}, // the one-time window comes first
		{h(1, 10, 0), true, h(1, 10, 0), h(1, 13, 0)},
		{h(1, 13, 0), false, h(1, 23, 0), time.Time{}},
		{h(4, 23, 30), true, h(4, 23, 30), h(5, 8, 0)}, // Friday night runs into Saturday, longer than 07:00
		{h(5, 8, 0), false, h(6, 0, 0), time.Time{}},   // Saturday day: Sunday 00:00 next
		{h(6, 6, 0), true, h(6, 6, 0), h(6, 7, 0)},
		{h(6, 7, 0), false, h(7, 23, 0), time.Time{}}, // Sunday: Monday night is the next
	} {
		if got := w.Open(c.t); got != c.open {
			t.Errorf("%s: open %v", c.t.Format("Mon 15:04"), got)
		}
		if got := w.Next(c.t); !got.Equal(c.next) {
			t.Errorf("%s: next %s, want %s", c.t.Format("Mon 15:04"), got.Format("Mon 15:04"), c.next.Format("Mon 15:04"))
		}
		if got := w.End(c.t); !got.Equal(c.end) {
			t.Errorf("%s: end %s, want %s", c.t.Format("Mon 15:04"), got.Format("Mon 15:04"), c.end.Format("Mon 15:04"))
		}
	}
	if got := w.String(); got != "Mo–Fr 23:00–08:00, Sa, So 00:00–07:00, einmalig 03.11. 09:00–13:00" {
		t.Errorf("text %q", got)
	}
	if got := w.TextAt(h(1, 13, 0)); got != "Mo–Fr 23:00–08:00, Sa, So 00:00–07:00" {
		t.Errorf("text after the one-time window %q", got)
	}
	if got := w.TextAt(h(1, 12, 0)); !strings.HasSuffix(got, "einmalig 03.11. 09:00–13:00") {
		t.Errorf("text during the one-time window %q", got)
	}
	if len(w.Expired(h(1, 12, 0))) != 0 || len(w.Expired(h(1, 13, 0))) != 1 {
		t.Error("expired")
	}
	// a one-time window without weekly rules: any time anyway
	if !(Window{Extra: []Span{once}}).Any() {
		t.Error("only one-time windows")
	}
}

func TestParseDaysAndSpans(t *testing.T) {
	for in, want := range map[string]Days{
		"mon-fri": 0b0111110, "sat,sun": 0b1000001, "Mo,Mi,Fr": 0b0101010, "fri-mon": 0b1100011, "": AllDays, "täglich": AllDays, "so": 1,
	} {
		if got, err := ParseDays(in); err != nil || got != want {
			t.Errorf("ParseDays(%q) = %07b, %v", in, got, err)
		}
	}
	if _, err := ParseDays("feiertags"); err == nil {
		t.Error("feiertags accepted")
	}
	if (Days(0b0111110)).String() != "Mo–Fr" || (Days(0b1000001)).String() != "Sa, So" || (Days(0b0101010)).String() != "Mo, Mi, Fr" {
		t.Error("days text")
	}
	s, err := ParseSpan("2026-11-03 22:00", "02:00", time.Local) // past midnight
	if err != nil || s.To.Day() != 4 {
		t.Errorf("span over midnight: %v %v", s, err)
	}
	if s, err := ParseSpan("2026-11-03T09:00", "2026-11-05 09:00", time.Local); err != nil || s.To.Sub(s.From) != 48*time.Hour {
		t.Errorf("long span: %v %v", s, err)
	}
	for _, bad := range [][2]string{{"morgen", "13:00"}, {"2026-11-03 09:00", "2026-11-03 08:00"}, {"2026-11-03 09:00", "bald"}} {
		if _, err := ParseSpan(bad[0], bad[1], time.Local); err == nil {
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
	open := Daily((m+1439)%1440, (m+1)%1440)
	wctx, cancel, ok := open.Wait(ctx)
	if !ok || wctx.Err() != nil {
		t.Fatal("open window waits")
	}
	cancel()
	if !Closed(ctx, wctx) {
		t.Error("cancelled window context")
	}
	// closed now: waits until ctx ends
	closed := Daily((m+120)%1440, (m+180)%1440)
	c2, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if _, _, ok := closed.Wait(c2); ok {
		t.Error("closed window ran")
	}
	// a one-time window open now opens a closed week
	closed.Extra = []Span{{From: now.Add(-time.Minute), To: now.Add(time.Hour)}}
	if _, cancel, ok := closed.Wait(c2); !ok {
		t.Error("one-time window did not open")
	} else {
		cancel()
	}
}

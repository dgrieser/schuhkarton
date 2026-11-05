// Package schedule keeps background work inside time windows, like 23:00 to
// 08:00 on weekdays and 00:00 to 07:00 at the weekend, plus one-time windows
// (today 09:00 to 13:00), so that it does not compete with people looking at
// photos or with the network at other times.
package schedule

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Window is when background work may run: weekly rules and one-time spans.
// Without rules it may run at any time (spans then add nothing). Each rule
// and span may allow only some kinds of work (Jobs); For gives the window of
// one kind.
type Window struct {
	Rules []Rule
	Extra []Span
	// limited: the window of one kind of work (For); without rules of its
	// own it runs only in its spans, never at any time
	limited bool
}

// Rule is a daily span of local time on some weekdays. A rule may pass
// midnight (23:00 to 08:00); it belongs to the day it starts on.
type Rule struct {
	Days     Days
	From, To int      // minutes after midnight, From != To
	Jobs     []string // the kinds of work allowed (see Jobs); empty: all
}

// Span is a one-time window.
type Span struct {
	From, To time.Time
	Jobs     []string // the kinds of work allowed (see Jobs); empty: all
}

// The kinds of background work a window can allow.
const (
	JobScan     = "scan"     // reading the folders again
	JobMedia    = "media"    // thumbnails, previews, faces
	JobAI       = "ai"       // descriptions
	JobOCR      = "ocr"      // text recognition
	JobSpeech   = "speech"   // speech recognition
	JobMetadata = "metadata" // writing into the files
	JobDupes    = "dupes"    // comparing for duplicates
	JobBackup   = "backup"   // backups
	JobCache    = "cache"    // moving large cache files to the overflow folder
	JobImport   = "import"   // filing the import folder
)

// Job is one kind of background work with its name in the settings.
type Job struct{ Key, Label string }

// Jobs lists the kinds of background work in the order shown.
var Jobs = []Job{
	{JobScan, "Einlesen"},
	{JobMedia, "Vorschaubilder und Gesichter"},
	{JobAI, "Beschreibungen"},
	{JobOCR, "Texterkennung"},
	{JobSpeech, "Spracherkennung"},
	{JobMetadata, "Metadaten schreiben"},
	{JobDupes, "Duplikate"},
	{JobBackup, "Sicherung"},
	{JobCache, "Cache auslagern"},
	{JobImport, "Import"},
}

// ParseJobs checks names of kinds of work; empty means all.
func ParseJobs(names []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || seen[n] {
			continue
		}
		known := false
		for _, j := range Jobs {
			known = known || j.Key == n
		}
		if !known {
			keys := make([]string, len(Jobs))
			for i, j := range Jobs {
				keys[i] = j.Key
			}
			return nil, fmt.Errorf("%q: unknown kind of work (%s)", n, strings.Join(keys, ", "))
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == len(Jobs) {
		return nil, nil // all
	}
	return out, nil
}

func allows(jobs []string, job string) bool {
	if len(jobs) == 0 {
		return true
	}
	for _, j := range jobs {
		if j == job {
			return true
		}
	}
	return false
}

// For returns the window of one kind of work: the rules and spans that
// allow it. Without any rules (any time) it is w itself.
func (w Window) For(job string) Window {
	if w.Any() {
		return w
	}
	out := Window{limited: true}
	for _, r := range w.Rules {
		if allows(r.Jobs, job) {
			out.Rules = append(out.Rules, r)
		}
	}
	for _, s := range w.Extra {
		if allows(s.Jobs, job) {
			out.Extra = append(out.Extra, s)
		}
	}
	return out
}

// jobsText is " (nur Sicherung, Duplikate)" for a window that allows only
// some kinds of work.
func jobsText(jobs []string) string {
	if len(jobs) == 0 {
		return ""
	}
	var names []string
	for _, j := range Jobs {
		if allows(jobs, j.Key) {
			names = append(names, j.Label)
		}
	}
	return " (nur " + strings.Join(names, ", ") + ")"
}

// Days is a set of weekdays (bit time.Sunday ... time.Saturday).
type Days uint8

// AllDays is every day of the week.
const AllDays Days = 1<<7 - 1

func (d Days) Has(w time.Weekday) bool { return d&(1<<w) != 0 }

// Daily is one rule for every day (minutes after midnight).
func Daily(from, to int) Window {
	if from == to {
		return Window{}
	}
	return Window{Rules: []Rule{{Days: AllDays, From: from, To: to}}}
}

// Parse reads one daily window "HH:MM"–"HH:MM"; two empty strings mean any
// time.
func Parse(from, to string) (Window, error) {
	r, err := ParseRule("", from, to)
	if err != nil || r == nil {
		return Window{}, err
	}
	return Window{Rules: []Rule{*r}}, nil
}

// ParseRule reads a rule: days like "mon-fri", "sat,sun", "mo-fr", "täglich"
// (empty: every day) and the times; nil when both times are empty.
func ParseRule(days, from, to string) (*Rule, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" && to == "" {
		if strings.TrimSpace(days) != "" {
			return nil, fmt.Errorf("%s: need a start and an end time", days)
		}
		return nil, nil
	}
	if from == "" || to == "" {
		return nil, fmt.Errorf("need a start and an end time")
	}
	f, err := clock(from)
	if err != nil {
		return nil, err
	}
	t, err := clock(to)
	if err != nil {
		return nil, err
	}
	if f == t {
		return nil, fmt.Errorf("start and end are the same; leave both empty for any time")
	}
	d, err := ParseDays(days)
	if err != nil {
		return nil, err
	}
	return &Rule{Days: d, From: f, To: t}, nil
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "su": time.Sunday, "so": time.Sunday, "sonntag": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "mo": time.Monday, "montag": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tu": time.Tuesday, "di": time.Tuesday, "dienstag": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "we": time.Wednesday, "mi": time.Wednesday, "mittwoch": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "th": time.Thursday, "do": time.Thursday, "donnerstag": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "fr": time.Friday, "freitag": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "sa": time.Saturday, "samstag": time.Saturday, "saturday": time.Saturday,
}

// ParseDays reads "mon-fri", "sat,sun", "mo,mi,fr", "fri-mon" or "daily"
// (also "täglich", "*", empty).
func ParseDays(s string) (Days, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "*", "daily", "täglich", "all", "alle":
		return AllDays, nil
	}
	var d Days
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		a, b, isRange := strings.Cut(part, "-")
		from, ok1 := dayNames[strings.TrimSpace(a)]
		to := from
		ok2 := true
		if isRange {
			to, ok2 = dayNames[strings.TrimSpace(b)]
		}
		if !ok1 || !ok2 {
			return 0, fmt.Errorf("%q: days like mon-fri or sat,sun", part)
		}
		for w := from; ; w = (w + 1) % 7 {
			d |= 1 << w
			if w == to {
				break
			}
		}
	}
	return d, nil
}

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is no time like 23:00", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// ParseSpan reads a one-time window: a start like "2026-11-03 09:00" and an
// end time "13:00" on the same day (or the next, when it is earlier), or a
// full end like "2026-11-04 08:00".
func ParseSpan(from, to string, loc *time.Location) (Span, error) {
	f, err := parseWhen(from, loc)
	if err != nil {
		return Span{}, err
	}
	var t time.Time
	if m, err := clock(strings.TrimSpace(to)); err == nil {
		y, mo, d := f.Date()
		t = time.Date(y, mo, d, m/60, m%60, 0, 0, loc)
		if !t.After(f) {
			t = t.AddDate(0, 0, 1)
		}
	} else if t, err = parseWhen(to, loc); err != nil {
		return Span{}, err
	}
	if !t.After(f) {
		return Span{}, fmt.Errorf("%s–%s: the end is not after the start", from, to)
	}
	return Span{From: f, To: t}, nil
}

func parseWhen(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02T15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is no date and time like 2026-11-03 09:00", s)
}

// Any tells whether work may run at any time.
func (w Window) Any() bool { return len(w.Rules) == 0 && !w.limited }

// at returns the day of t at minute m.
func at(t time.Time, m int) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d, m/60, m%60, 0, 0, t.Location())
}

// spans lists the windows that touch the days around t (a few days back
// for rules that pass midnight, n days ahead), one-time ones included.
func (w Window) spans(t time.Time, ahead int) []Span {
	var out []Span
	day := at(t, 0)
	for i := -1; i <= ahead; i++ {
		d := day.AddDate(0, 0, i)
		for _, r := range w.Rules {
			if !r.Days.Has(d.Weekday()) {
				continue
			}
			s := Span{From: at(d, r.From), To: at(d, r.To)}
			if r.To <= r.From {
				s.To = at(d.AddDate(0, 0, 1), r.To)
			}
			out = append(out, s)
		}
	}
	for _, s := range w.Extra {
		if s.To.After(t) {
			out = append(out, s)
		}
	}
	return out
}

func (s Span) has(t time.Time) bool { return !t.Before(s.From) && t.Before(s.To) }

// Open tells whether work may run at t.
func (w Window) Open(t time.Time) bool {
	if w.Any() {
		return true
	}
	for _, s := range w.spans(t, 0) {
		if s.has(t) {
			return true
		}
	}
	return false
}

// Next returns when work may run next: t itself while it may.
func (w Window) Next(t time.Time) time.Time {
	if w.Open(t) {
		return t
	}
	var best time.Time
	for _, s := range w.spans(t, 8) {
		if s.From.After(t) && (best.IsZero() || s.From.Before(best)) {
			best = s.From
		}
	}
	return best
}

// End returns when the window open at t closes, windows that meet or
// overlap counted as one (the zero time for any time or a closed window).
func (w Window) End(t time.Time) time.Time {
	if w.Any() || !w.Open(t) {
		return time.Time{}
	}
	spans := w.spans(t, 8)
	end := t
	for grew := true; grew; {
		grew = false
		for _, s := range spans {
			if !s.From.After(end) && s.To.After(end) {
				end, grew = s.To, true
			}
		}
	}
	return end
}

// Expired drops the one-time windows that are over.
func (w Window) Expired(now time.Time) []Span {
	var out []Span
	for _, s := range w.Extra {
		if !s.To.After(now) {
			out = append(out, s)
		}
	}
	return out
}

var dayShort = [7]string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}

// DaysText is a set of days as "Mo–Fr", "Sa, So" or "täglich".
func (d Days) String() string {
	if d == AllDays {
		return "täglich"
	}
	order := []time.Weekday{1, 2, 3, 4, 5, 6, 0}
	var parts []string
	for i := 0; i < 7; {
		if !d.Has(order[i]) {
			i++
			continue
		}
		j := i
		for j+1 < 7 && d.Has(order[j+1]) {
			j++
		}
		switch {
		case j == i:
			parts = append(parts, dayShort[order[i]])
		case j == i+1:
			parts = append(parts, dayShort[order[i]], dayShort[order[j]])
		default:
			parts = append(parts, dayShort[order[i]]+"–"+dayShort[order[j]])
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}

func hm(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

func (w Window) String() string { return w.TextAt(time.Time{}) }

// TextAt is the window as text without the one-time windows that are over
// at now (zero now: all of them). A running program keeps its window until
// a restart, while the settings file loses the past ones at once.
func (w Window) TextAt(now time.Time) string {
	if w.Any() {
		return ""
	}
	var parts []string
	for _, r := range w.Rules {
		s := hm(r.From) + "–" + hm(r.To)
		if r.Days != AllDays {
			s = r.Days.String() + " " + s
		}
		parts = append(parts, s+jobsText(r.Jobs))
	}
	extra := append([]Span(nil), w.Extra...)
	sort.Slice(extra, func(a, b int) bool { return extra[a].From.Before(extra[b].From) })
	for _, s := range extra {
		if !now.IsZero() && !s.To.After(now) {
			continue
		}
		parts = append(parts, "einmalig "+s.From.Format("02.01. 15:04")+"–"+s.To.Format("15:04")+jobsText(s.Jobs))
	}
	return strings.Join(parts, ", ")
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
		d := poll
		if n := w.Next(time.Now()); !n.IsZero() {
			d = min(poll, time.Until(n)+time.Second)
		}
		t := time.NewTimer(d)
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

package gallery

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgrieser/schuhkarton/internal/schedule"
)

// remoteJob sends files one by one to an OpenAI-compatible service in the
// background: descriptions (ai.go) and text recognition (ocr.go). It keeps
// the progress for the UI, pauses while the service is busy and stops when
// the settings are wrong.
type remoteJob struct {
	name                     string    // for the log: "descriptions", "text recognition"
	kind                     string    // its kind of work in the background windows (schedule.JobAI …)
	client                   *aiClient // nil: off (remote jobs)
	on                       bool      // a job without a client (metadata) runs
	wake                     chan struct{}
	err                      atomic.Value // string: why the job stopped or pauses (key, model, rate limit …)
	done, todo, fail, tokens atomic.Int64
	paused                   atomic.Int64 // unix time the job waits until after a busy service; 0 while it runs
	rate                     jobRate
	urgent                   urgentSet     // files asked for by hand: not waiting for the window (urgent.go)
	urgentWake               chan struct{} // a job waiting for its window looks at urgent work
}

// jobTask is what a job works on.
type jobTask struct {
	workers  int
	eligible func(*Item) bool
	// state tells whether a file is handled already (a result, or a failure
	// that is not retried yet) and whether that was a failure.
	state func(it *Item, now time.Time) (handled, failed bool)
	// one works on one file. It returns an error only when the whole pass
	// has to stop (service problems); problems with the file are recorded.
	one  func(ctx context.Context, it *Item) error
	save func() error // keeps the results (an error: they are not kept)
	// first, when set, is asked once per pass for a test that puts the
	// files it returns true for at the front (cheap ones first:
	// descriptions of photos with a large preview)
	first func() func(*Item) bool
	// reads, when set, returns the files the work reads (else the file
	// itself): one the share hangs on leaves the work out
	reads func(*Item) []*Item
	// avoid, when set, tells instead of reads whether the work is left out
	avoid func(*Item) bool
}

// blocked tells whether a file the work reads is left out (the share hangs
// on it).
func (l *Library) blocked(t jobTask, it *Item) bool {
	if t.avoid != nil {
		return t.avoid(it)
	}
	if t.reads == nil {
		return l.unreadable(it) != nil
	}
	for _, r := range t.reads(it) {
		if l.unreadable(r) != nil {
			return true
		}
	}
	return false
}

func (j *remoteJob) kick() {
	if j.client == nil && !j.on {
		return
	}
	select {
	case j.wake <- struct{}{}:
	default:
	}
}

// handled counts one file that was worked on.
func (j *remoteJob) handled(failed bool, tokens int) {
	if failed {
		j.fail.Add(1)
	}
	j.tokens.Add(int64(tokens))
	j.done.Add(1)
	j.rate.add(time.Now())
}

// jobStop returns the error that stops a pass (cancelled, wrong settings,
// busy service), or nil when only this file failed.
func jobStop(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae *aiError
	if errors.As(err, &ae) && (ae.fatal || ae.transient) {
		return err
	}
	return nil
}

// jobLoop runs passes until ctx ends: after a kick (new files from a scan),
// now and then, and again after a pause.
func (l *Library) jobLoop(ctx context.Context, j *remoteJob, t jobTask) {
	defer l.bg.Done()
	backoff := time.Minute
	timer := time.NewTimer(3 * time.Second) // thumbnails first
	defer timer.Stop()
	// urgent work that failed (the service down for a moment) is tried again
	// outside the window, waiting longer each time
	urgentBackoff := time.Minute
	var urgentRetry *time.Timer
	defer func() {
		if urgentRetry != nil {
			urgentRetry.Stop()
		}
	}()
	j.rate.begin(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-j.wake:
		case <-timer.C:
		}
		win := l.window(j.kind)
		if !win.Open(time.Now()) {
			if j.hasUrgent() { // asked for by hand: right away, not in the window
				err := l.jobPass(ctx, j, j.urgentTask(t))
				if err != nil && ctx.Err() == nil {
					l.logf("%s (asked for): %v", j.name, err)
				}
				if l.saveJob(j, t) == nil { // the results first: then the requests done are forgotten
					l.pruneUrgent(j, t)
				}
				if ctx.Err() != nil {
					return
				}
				if urgentRetry != nil {
					urgentRetry.Stop()
				}
				if err == nil {
					urgentBackoff = time.Minute
				} else if j.hasUrgent() {
					urgentRetry = time.AfterFunc(urgentBackoff, func() {
						select {
						case j.urgentWake <- struct{}{}:
						default:
						}
					})
					urgentBackoff = min(urgentBackoff*2, 30*time.Minute)
				}
			}
			j.paused.Store(0) // no old pause shown: the window decides now
			l.jobTodo(j, t)   // the counts for the UI meanwhile
		}
		// waiting for the window; urgent work asked for meanwhile does not wait
		waitCtx, stopWait := context.WithCancel(ctx)
		waiting := make(chan struct{}) // closed when the wait ends: wake-ups are the loop's again
		if !win.Open(time.Now()) {
			go l.urgentWaiter(waitCtx, j, waiting, stopWait)
		}
		wctx, cancel, ok := win.Wait(waitCtx)
		close(waiting)
		if !ok {
			stopWait()
			if ctx.Err() != nil {
				l.saveJob(j, t)
				return
			}
			j.kick() // urgent work: round again
			continue
		}
		j.paused.Store(0) // running again: no pause, no old error
		j.err.Store("")
		err := l.jobPass(wctx, j, t)
		closed := schedule.Closed(ctx, wctx)
		cancel()
		stopWait()
		if j.hasUrgent() {
			if l.saveJob(j, t) == nil { // the results first: then the requests done are forgotten
				l.pruneUrgent(j, t)
			}
		}
		if ctx.Err() != nil {
			l.saveJob(j, t)
			return
		}
		wait := 20 * time.Minute // look again now and then (new photos come with scans, which kick)
		var ae *aiError
		switch {
		case closed:
			wait = time.Second // waits for the next window
		case err == nil:
			backoff = time.Minute
		case errors.As(err, &ae) && ae.fatal:
			j.err.Store(ae.Error())
			l.logf("%s stopped: %v", j.name, err)
			wait = time.Hour
		default:
			j.err.Store(err.Error())
			l.logf("%s paused for %s: %v", j.name, backoff, err)
			wait, backoff = backoff, min(backoff*2, 30*time.Minute)
			j.paused.Store(time.Now().Add(wait).Unix())
		}
		l.saveJob(j, t)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
	}
}

// jobPass works on every eligible file that is not handled yet, newest
// first. It returns early on errors that concern the service, not a file.
func (l *Library) jobPass(ctx context.Context, j *remoteJob, t jobTask) error {
	todo := l.jobTodo(j, t)
	if len(todo) == 0 {
		return nil
	}
	if t.first != nil { // one look per file, the order kept otherwise
		first := t.first()
		var front, back []*Item
		for _, it := range todo {
			if first(it) {
				front = append(front, it)
			} else {
				back = append(back, it)
			}
		}
		todo = append(front, back...)
	}
	l.logf("%s: %d to do", j.name, len(todo))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan *Item)
	var wg sync.WaitGroup
	var once sync.Once
	var stopErr error
	for range max(1, t.workers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				if l.blocked(t, it) { // left out since the list was made
					j.handled(true, 0)
					continue
				}
				if err := t.one(ctx, it); err != nil {
					once.Do(func() { stopErr = err; cancel() })
					return
				}
			}
		}()
	}
	for _, it := range todo {
		select {
		case ch <- it:
			continue
		case <-ctx.Done():
		}
		break
	}
	close(ch)
	wg.Wait()
	if stopErr != nil && errors.Is(stopErr, context.Canceled) {
		stopErr = nil
	}
	return stopErr
}

// jobTodo returns the files a job has still to work on and stores the
// counts for the UI.
func (l *Library) jobTodo(j *remoteJob, t jobTask) []*Item {
	var todo []*Item
	var total, have, failed int64
	now := time.Now()
	for _, it := range l.Items() {
		if !t.eligible(it) {
			continue
		}
		total++
		switch done, bad := t.state(it, now); {
		case done:
			have++
			if bad {
				failed++
			}
		case l.blocked(t, it): // the share hangs on it: left out, counted as failed
			have++
			failed++
		default:
			todo = append(todo, it)
		}
	}
	j.todo.Store(total)
	j.done.Store(have)
	j.fail.Store(failed)
	return todo
}

// jobRate measures how many files a job handles per hour, over the last
// hour and pauses included, for the remaining time shown in the UI.
type jobRate struct {
	mu    sync.Mutex
	start time.Time   // when the job started
	times []time.Time // files handled in the last hour
}

func (r *jobRate) begin(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.start, r.times = now, nil
}

func (r *jobRate) add(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trim(now)
	r.times = append(r.times, now)
}

func (r *jobRate) trim(now time.Time) {
	i := 0
	for i < len(r.times) && now.Sub(r.times[i]) > time.Hour {
		i++
	}
	r.times = r.times[i:]
}

// perHour is the rate, or 0 while there is too little to tell (the first
// five minutes, nothing handled).
func (r *jobRate) perHour(now time.Time) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trim(now)
	span := min(time.Hour, now.Sub(r.start))
	if r.start.IsZero() || span < 5*time.Minute || len(r.times) == 0 {
		return 0
	}
	return float64(len(r.times)) / span.Hours()
}

// urgentWaiter ends the wait for the window (stop) when urgent work comes, or
// on a wake-up while urgent work waits (a video for its words); it returns
// when the wait ends (waiting closed). Other wake-ups need no pass before
// the window opens.
func (l *Library) urgentWaiter(waitCtx context.Context, j *remoteJob, waiting <-chan struct{}, stop func()) {
	for {
		select {
		case <-waiting:
			return
		case <-j.urgentWake:
			stop()
			return
		case <-j.wake:
			if j.hasUrgent() {
				stop()
				return
			}
		case <-waitCtx.Done():
			return
		}
	}
}

// saveJob keeps the results of a job (an error is logged and returned).
func (l *Library) saveJob(j *remoteJob, t jobTask) error {
	err := t.save()
	if err != nil {
		l.logf("%s: %v", j.name, err)
	}
	return err
}

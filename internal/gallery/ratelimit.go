package gallery

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// A service like mittwald's AI hosting allows a number of requests per
// minute for the whole account (x-ratelimit-user-limit-requests: 30 and
// …-remaining-requests in every answer; the count starts again every
// minute). Descriptions, text and speech recognition share it, so they
// share one budget: requests are sent while some are left and wait for the
// next minute when none are, instead of running into HTTP 429 and pausing
// the job. A 429 anyway (another program uses the account too) waits for
// the next minute and sends the same request again.

type rateBudget struct {
	mu          sync.Mutex
	known       bool          // the service told its numbers
	remaining   int           // left in this minute (after the requests on their way)
	lastSeen    int           // remaining in the last answer
	inflight    int           // sent, no answer yet
	limit       int           // requests per window (x-ratelimit-user-limit-requests)
	windowStart time.Time     // when the count last started again (about)
	until       time.Time     // nothing is sent before
	seq         int64         // requests sent
	answered    bool          // an answer came: until then one request at a time (the numbers come with it)
	changed     chan struct{} // closed when a new window frees the waiting
}

// sendTicket is a request on its way: the window it went out in and how
// many requests went out before it.
type sendTicket struct {
	window time.Time
	seq    int64
}

// rateWindow is how long the service counts requests; rateKeep is how many
// of them are left for other programs on the account.
var (
	rateWindow  = 61 * time.Second
	rateKeep    = 1
	rateUnknown = 20 * time.Second // the wait after a 429 when the window is not known
	rateRetries = 5                // 429 for the same request: then the job pauses
)

var budgets sync.Map // URL + key -> *rateBudget

func budgetFor(url, key string) *rateBudget {
	b, _ := budgets.LoadOrStore(url+"\x00"+key, &rateBudget{})
	return b.(*rateBudget)
}

// wait blocks until a request may be sent and counts it. Its ticket goes
// to seen with the answer.
func (b *rateBudget) wait(ctx context.Context) (sendTicket, error) {
	for {
		b.mu.Lock()
		now := time.Now()
		b.roll(now)
		if b.known && b.remaining <= rateKeep && !now.Before(b.until) {
			// used up: until the count starts again
			b.until = b.nextWindow(now)
		}
		d := b.until.Sub(now)
		if d <= 0 && !b.answered && b.inflight > 0 {
			d = rateUnknown // the first answer frees it earlier
		}
		if d <= 0 {
			if b.known {
				b.remaining--
			}
			b.inflight++
			b.seq++
			tk := sendTicket{window: b.windowStart, seq: b.seq}
			b.mu.Unlock()
			return tk, nil
		}
		if b.changed == nil {
			b.changed = make(chan struct{})
		}
		changed := b.changed
		b.mu.Unlock()
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return sendTicket{}, ctx.Err()
		case <-t.C:
		case <-changed:
			t.Stop()
		}
	}
}

// roll starts a new window with its full budget when the known one is over
// (after a wait, or after a quiet while); the answers correct it.
func (b *rateBudget) roll(now time.Time) {
	if !b.known || b.windowStart.IsZero() || now.Before(b.windowStart.Add(rateWindow)) {
		return
	}
	for !now.Before(b.windowStart.Add(rateWindow)) {
		b.windowStart = b.windowStart.Add(rateWindow)
	}
	b.remaining = max(b.limit, rateKeep+1) - b.inflight
	b.lastSeen = b.remaining
}

func (b *rateBudget) nextWindow(now time.Time) time.Time {
	if b.windowStart.IsZero() {
		return now.Add(rateUnknown)
	}
	next := b.windowStart.Add(rateWindow)
	for !next.After(now) {
		next = next.Add(rateWindow)
	}
	return next
}

// seen takes the numbers of an answer (h nil: no answer) to the request of
// tk; limited: it was a 429.
func (b *rateBudget) seen(h http.Header, limited bool, tk sendTicket) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.inflight = max(0, b.inflight-1)
	later := b.seq - tk.seq // requests sent after this one
	if !b.answered && h != nil {
		b.answered = true
		b.wakeLocked()
	}
	if n, err := strconv.Atoi(h.Get("x-ratelimit-user-limit-requests")); err == nil && n > 0 {
		b.limit = n
	}
	v, err := strconv.Atoi(h.Get("x-ratelimit-user-remaining-requests"))
	switch {
	case err != nil:
	case !b.known:
		b.windowStart = now // not the real start, maybe later: on the safe side
		b.remaining, b.lastSeen = v-b.inflight, v
	case v > b.lastSeen+int(later) && (tk.window.IsZero() || !tk.window.Before(b.windowStart)):
		// more left than the requests sent after this one could explain
		// (answers come out of order): the count started again
		b.windowStart = now
		b.remaining, b.lastSeen = v-b.inflight, v
		if now.Before(b.until) { // the waiting may go
			b.until = now
			b.wakeLocked()
		}
	case !tk.window.IsZero() && tk.window.Before(b.windowStart):
		// an answer from the window before: its count says nothing now
	default:
		// answers may come out of order: the lowest count of the window wins
		b.remaining = min(b.remaining, v-b.inflight)
		b.lastSeen = min(b.lastSeen, v)
	}
	if err == nil {
		b.known = true
	}
	if limited && (tk.window.IsZero() || !tk.window.Before(b.windowStart)) {
		// (a 429 for a request of the window before says nothing now)
		b.remaining = 0
		b.until = b.nextWindow(now)
	}
}

// wakeLocked frees the requests waiting in wait (b.mu held).
func (b *rateBudget) wakeLocked() {
	if b.changed != nil {
		close(b.changed)
		b.changed = nil
	}
}

package gallery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A service that allows a few requests per window: requests wait for the
// next window instead of getting 429, and a 429 anyway is sent again.
func TestRateBudget(t *testing.T) {
	defer func(w, u time.Duration) { rateWindow, rateUnknown = w, u }(rateWindow, rateUnknown)
	// a window long enough that a busy CI runner's scheduling delays stay small
	rateWindow, rateUnknown = time.Second, time.Second
	const perWindow = 10
	var mu sync.Mutex
	start, used := time.Now(), 0
	var got429, served atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if time.Since(start) >= rateWindow {
			start, used = time.Now(), 0
		}
		used++
		left := perWindow - used
		mu.Unlock()
		w.Header().Set("x-ratelimit-user-limit-requests", strconv.Itoa(perWindow))
		w.Header().Set("x-ratelimit-user-remaining-requests", strconv.Itoa(max(0, left)))
		if left < 0 {
			got429.Add(1)
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"message":"Rate limit exceeded"}}`))
			return
		}
		served.Add(1)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := newAIClient(AIOptions{URL: srv.URL, Key: "k"})
	var wg sync.WaitGroup
	errs := make(chan error, 30)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				var out map[string]any
				if err := c.send(context.Background(), "POST", "/x", "application/json", []byte(`{}`), &out); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("request failed: %v", err)
	}
	if served.Load() != 30 {
		t.Errorf("served %d of 30", served.Load())
	}
	// without the budget most of the 30 hit 429; with it a few: this server
	// starts its window with the first request after the last one ended
	t.Logf("429: %d", got429.Load())
	if n := got429.Load(); n > 8 {
		t.Errorf("%d answers were 429", n)
	}
}

// Always 429: given up after a few tries, so the job pauses.
func TestRateBudgetGivesUp(t *testing.T) {
	defer func(u time.Duration) { rateUnknown = u }(rateUnknown)
	rateUnknown = 10 * time.Millisecond
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"Rate limit exceeded"}}`))
	}))
	defer srv.Close()
	c := newAIClient(AIOptions{URL: srv.URL})
	var out map[string]any
	err := c.send(context.Background(), "POST", "/x", "application/json", []byte(`{}`), &out)
	if !rateLimited(err) || calls.Load() != int64(rateRetries) {
		t.Errorf("err %v after %d calls", err, calls.Load())
	}
}

// After a quiet minute the budget is full again; an answer from the window
// before does not empty the new one.
func TestRateBudgetWindows(t *testing.T) {
	hdr := func(left int) http.Header {
		h := http.Header{}
		h.Set("x-ratelimit-user-limit-requests", "30")
		h.Set("x-ratelimit-user-remaining-requests", strconv.Itoa(left))
		return h
	}
	b := &rateBudget{}
	ctx := context.Background()
	w0, _ := b.wait(ctx)
	b.seen(hdr(1), false, w0) // nearly used up
	b.mu.Lock()
	b.windowStart = b.windowStart.Add(-2 * rateWindow) // a quiet while
	b.mu.Unlock()
	start := time.Now()
	w1, _ := b.wait(ctx)
	if time.Since(start) > time.Second || b.remaining < 20 {
		t.Fatalf("not refreshed after a quiet window: %d left", b.remaining)
	}
	// an old request (sent in the window before) answers late, low
	b.seen(hdr(0), false, sendTicket{window: w1.window.Add(-rateWindow), seq: w1.seq})
	if b.remaining < 20 {
		t.Errorf("old answer emptied the new window: %d", b.remaining)
	}

	// out of order in one window: a late answer with more left is no reset
	c := &rateBudget{}
	t0, _ := c.wait(ctx)
	c.seen(hdr(11), false, t0)
	var tks []sendTicket
	for range 4 {
		tk, _ := c.wait(ctx)
		tks = append(tks, tk)
	}
	c.seen(hdr(7), false, tks[3])
	ws := c.windowStart
	c.seen(hdr(9), false, tks[1]) // two were sent after it: 9 fits
	if !c.windowStart.Equal(ws) || c.remaining > 7 {
		t.Errorf("late answer taken for a reset: %d left", c.remaining)
	}

	// waiting for the next window: an answer that shows the reset frees it
	d := &rateBudget{}
	first, _ := d.wait(ctx)
	d.seen(hdr(3), false, first)
	a, _ := d.wait(ctx)
	d.wait(ctx) // one left: the next one waits
	got := make(chan time.Duration)
	go func() { s := time.Now(); d.wait(ctx); got <- time.Since(s) }()
	time.Sleep(20 * time.Millisecond)
	d.seen(hdr(29), false, a) // the service counts again
	select {
	case dt := <-got:
		if dt > 5*time.Second {
			t.Errorf("freed after %v", dt)
		}
	case <-time.After(5 * time.Second):
		t.Error("the waiting request was not freed")
	}
}

// Until the first answer brings the numbers, one request at a time; a
// service without them is not held up after that.
func TestRateBudgetFirstAnswer(t *testing.T) {
	b := &rateBudget{}
	ctx := context.Background()
	first, _ := b.wait(ctx)
	got := make(chan struct{})
	go func() { b.wait(ctx); close(got) }()
	select {
	case <-got:
		t.Fatal("a second request before the first answer")
	case <-time.After(50 * time.Millisecond):
	}
	b.seen(http.Header{}, false, first) // no numbers at all
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("not freed by the first answer")
	}
	for range 5 { // no numbers: no limit
		start := time.Now()
		b.wait(ctx)
		if time.Since(start) > time.Second {
			t.Fatal("held up without numbers")
		}
	}
}

package gallery

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The state of the face service is waited for once; after that a slow
// service does not hold up the status: the known state answers and the
// service is asked again in the background.
func TestRemoteProbeInBackground(t *testing.T) {
	var slow atomic.Bool
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if slow.Load() {
			time.Sleep(500 * time.Millisecond)
		}
		w.Write([]byte(`{"detection":true,"recognition":true,"version":"t"}`))
	}))
	defer srv.Close()
	f := newRemoteFaces(srv.URL, "tok")
	if i := f.probe(); !i.Recognition || i.Error != "" {
		t.Fatalf("first answer: %+v", i)
	}
	slow.Store(true)
	f.mu.Lock()
	f.infoAt = time.Now().Add(-2 * time.Minute)
	f.mu.Unlock()
	start := time.Now()
	if i := f.probe(); !i.Recognition {
		t.Errorf("known state lost: %+v", i)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("probe waited %v for a slow service", d)
	}
	f.probe() // while it is being asked: no second request
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		fresh := time.Since(f.infoAt) < time.Minute && !f.refreshing
		f.mu.Unlock()
		if fresh {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the state was not asked again in the background")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := asked.Load(); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

package worker

import "testing"

func TestFitMemory(t *testing.T) {
	const gb = 1 << 30
	for _, c := range []struct {
		limit           int64
		workers, aura   int
		wantW, wantAura int
	}{
		{0, 12, 2, 12, 2},      // no limit
		{2 * gb, 12, 1, 2, 1},  // 2 GB: one second-model process, two main ones
		{1 * gb, 12, 1, 1, 0},  // 1 GB: no room for the second model
		{8 * gb, 12, 2, 12, 2}, // room for all
		{3 * gb, 12, 2, 3, 2},
		{4 * gb, 4, 1, 4, 1},    // mittwald: 4 vCPUs (capped before), 4 GB
		{512 << 20, 4, 1, 1, 0}, // too little: one main process anyway
	} {
		w, a := fitMemory(c.limit, c.workers, c.aura)
		if w != c.wantW || a != c.wantAura {
			t.Errorf("limit %d MB, %d+%d: got %d+%d, want %d+%d", c.limit>>20, c.workers, c.aura, w, a, c.wantW, c.wantAura)
		}
	}
}

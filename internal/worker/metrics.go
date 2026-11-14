package worker

import (
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// metrics are counted for GET /v1/metrics: for debugging only (how busy the
// container is, what takes how long, whether processes die).
type metrics struct {
	start    time.Time
	inflight atomic.Int64
	mu       sync.Mutex
	status   map[int]int64
	times    map[string]*ring // seconds, the last ones per step
	faces    map[string]int64 // faces answered, by source ("main", "extra", "box")
	requests int64
}

const ringSize = 500

type ring struct {
	v []float64
	i int
	n int64
}

func (r *ring) add(x float64) {
	if len(r.v) < ringSize {
		r.v = append(r.v, x)
	} else {
		r.v[r.i] = x
		r.i = (r.i + 1) % ringSize
	}
	r.n++
}

func newMetrics() *metrics {
	return &metrics{start: time.Now(), status: map[int]int64{}, times: map[string]*ring{}, faces: map[string]int64{}}
}

// took notes how long a step took.
func (m *metrics) took(step string, since time.Time) {
	d := time.Since(since).Seconds()
	m.mu.Lock()
	r := m.times[step]
	if r == nil {
		r = &ring{}
		m.times[step] = r
	}
	r.add(d)
	m.mu.Unlock()
}

func (m *metrics) done(code int) {
	m.mu.Lock()
	m.status[code]++
	m.requests++
	m.mu.Unlock()
}

func (m *metrics) found(src string, n int) {
	m.mu.Lock()
	m.faces[src] += int64(n)
	m.mu.Unlock()
}

type timing struct {
	Count int64   `json:"count"`
	Last  int     `json:"last"` // of the values below
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	Max   float64 `json:"max"`
	Mean  float64 `json:"mean"`
}

func (m *metrics) snapshot() map[string]any {
	m.mu.Lock()
	times := map[string]timing{}
	for k, r := range m.times {
		v := append([]float64(nil), r.v...)
		sort.Float64s(v)
		t := timing{Count: r.n, Last: len(v)}
		if len(v) > 0 {
			var sum float64
			for _, x := range v {
				sum += x
			}
			t.P50, t.P95, t.Max, t.Mean = v[len(v)/2], v[min(len(v)-1, len(v)*95/100)], v[len(v)-1], sum/float64(len(v))
		}
		times[k] = t
	}
	status := map[string]int64{}
	for k, v := range m.status {
		status[strconv.Itoa(k)] = v
	}
	faces := map[string]int64{}
	for k, v := range m.faces {
		faces[k] = v
	}
	req := m.requests
	m.mu.Unlock()
	return map[string]any{
		"uptimeSeconds": int64(time.Since(m.start).Seconds()),
		"requests":      map[string]any{"total": req, "inflight": m.inflight.Load(), "status": status},
		"seconds":       times,
		"faces":         faces,
	}
}

// container reads the cgroup's limits and use (Linux).
func container() map[string]any {
	out := map[string]any{"cpus": runtime.NumCPU(), "cpuLimit": cpuLimit(), "memoryLimitMB": memLimit() >> 20}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	mb := func(s string) any {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil
		}
		return v >> 20
	}
	if v := read("/sys/fs/cgroup/memory.current"); v != "" {
		out["memoryMB"] = mb(v)
		out["memoryPeakMB"] = mb(read("/sys/fs/cgroup/memory.peak"))
		ev := map[string]int64{}
		for _, line := range strings.Split(read("/sys/fs/cgroup/memory.events"), "\n") {
			if f := strings.Fields(line); len(f) == 2 {
				ev[f[0]], _ = strconv.ParseInt(f[1], 10, 64)
			}
		}
		out["memoryEvents"] = ev // oom_kill: processes killed for memory
	} else if v := read("/sys/fs/cgroup/memory/memory.usage_in_bytes"); v != "" {
		out["memoryMB"] = mb(v)
		out["memoryPeakMB"] = mb(read("/sys/fs/cgroup/memory/memory.max_usage_in_bytes"))
	}
	if v := read("/sys/fs/cgroup/cpu.stat"); v != "" {
		st := map[string]int64{}
		for _, line := range strings.Split(v, "\n") {
			if f := strings.Fields(line); len(f) == 2 {
				st[f[0]], _ = strconv.ParseInt(f[1], 10, 64)
			}
		}
		out["cpuStat"] = st // nr_throttled, throttled_usec: time held back by the CPU limit
	}
	if v := read("/proc/loadavg"); v != "" {
		out["loadavg"] = v
	}
	return out
}

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	out := s.m.snapshot()
	out["version"] = s.Info().Version
	out["container"] = container()
	pools := map[string]any{"main": s.pool.Stats()}
	if s.aura != nil {
		pools["aura"] = s.aura.Stats()
	}
	out["pools"] = pools
	out["config"] = map[string]any{"workers": s.opt.Workers, "auraWorkers": s.opt.AuraWorkers, "minScore": s.opt.MinScore}
	writeJSON(w, out)
}

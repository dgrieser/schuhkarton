package worker

import (
	"os"
	"strconv"
	"strings"
)

// Memory a process takes, measured with real photos (1600 px, both
// detectors): the main processes 250-490 MB, one with the second
// recognition model about 500 MB, the server itself some 50 MB.
const (
	memMain = 500 << 20
	memAura = 550 << 20
	memBase = 200 << 20
)

// memLimit is the memory the container may use (cgroup v2 or v1); 0:
// unknown or unlimited.
func memLimit() int64 {
	for _, p := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil || v <= 0 || v >= 1<<50 { // "max", or the v1 "no limit" value
			return 0
		}
		return v
	}
	return 0
}

// fitMemory caps the processes to what fits into limit (0: no cap): the
// second model keeps one process when one main process fits beside it,
// the main processes take the rest, at least one.
func fitMemory(limit int64, workers, aura int) (int, int) {
	if limit <= 0 {
		return workers, aura
	}
	free := limit - memBase
	if aura > 0 && free < memAura+memMain {
		aura = 0 // not even one of each: the main search alone
	}
	for aura > 1 && free-int64(aura)*memAura < int64(aura)*memMain {
		aura--
	}
	free -= int64(aura) * memAura
	workers = max(1, min(workers, int(free/memMain)))
	return workers, aura
}

// cpuLimit is the number of CPUs the container may use (cgroup v2 cpu.max
// or v1 quota/period, rounded up); 0: unknown or unlimited. A container
// often sees all CPUs of its host, but runs on its share.
func cpuLimit() int {
	if b, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		f := strings.Fields(string(b))
		if len(f) == 2 && f[0] != "max" {
			q, err1 := strconv.ParseInt(f[0], 10, 64)
			p, err2 := strconv.ParseInt(f[1], 10, 64)
			if err1 == nil && err2 == nil && q > 0 && p > 0 {
				return int((q + p - 1) / p)
			}
		}
		return 0
	}
	qb, err1 := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	pb, err2 := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if err1 != nil || err2 != nil {
		return 0
	}
	q, _ := strconv.ParseInt(strings.TrimSpace(string(qb)), 10, 64)
	p, _ := strconv.ParseInt(strings.TrimSpace(string(pb)), 10, 64)
	if q <= 0 || p <= 0 {
		return 0
	}
	return int((q + p - 1) / p)
}

package main

import (
	"bufio"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// defaultMemoryLimit lets the Go runtime return memory before the system
// runs out: without GOMEMLIMIT the heap may grow to twice what is in use,
// which on a small machine (a Raspberry Pi with 2 GB) ends with the OOM
// killer. The soft limit is half the RAM; GOMEMLIMIT overrides it.
func defaultMemoryLimit() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	if total := totalMemory(); total > 0 {
		debug.SetMemoryLimit(total / 2)
	}
}

// totalMemory reads MemTotal from /proc/meminfo (Linux; 0 elsewhere).
func totalMemory() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			if err != nil {
				return 0
			}
			return kb << 10
		}
	}
	return 0
}

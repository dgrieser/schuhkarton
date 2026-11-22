package server

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// stopwatch notes how long the steps of a request took: a slow one is
// logged with them, the evidence of what it waited for.
type stopwatch struct {
	start, last time.Time
	steps       []string
}

func newStopwatch() *stopwatch {
	now := time.Now()
	return &stopwatch{start: now, last: now}
}

// lap ends a step.
func (s *stopwatch) lap(step string) {
	now := time.Now()
	s.steps = append(s.steps, fmt.Sprintf("%s %s", step, now.Sub(s.last).Round(10*time.Millisecond)))
	s.last = now
}

// logSlow logs the steps when the whole took at least limit.
func (s *stopwatch) logSlow(what string, limit time.Duration) {
	if d := time.Since(s.start); d >= limit {
		log.Printf("slow %s (%s): %s", what, d.Round(100*time.Millisecond), strings.Join(s.steps, ", "))
	}
}

package gallery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Urgent work: files an account asked to be described or recognized again do
// not wait for the background window of their job; they run right away. The
// ones not done yet are kept in urgent.json, so a restart goes on with them.

type urgentSet struct {
	mu  sync.Mutex
	ids map[string]bool
}

var urgentFileMu sync.Mutex

func (l *Library) urgentPath() string { return filepath.Join(l.opt.CacheDir, "urgent.json") }

// urgentJobs are the jobs that take urgent work, by their kind.
func (l *Library) urgentJobs() map[string]*remoteJob {
	return map[string]*remoteJob{ChAI: &l.aiJob, ChOCR: &l.ocrJob, ChSpeech: &l.speechJob}
}

// loadUrgent takes what was asked for before a restart.
func (l *Library) loadUrgent() {
	b, err := os.ReadFile(l.urgentPath())
	if err != nil {
		return
	}
	var saved map[string][]string
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	for kind, j := range l.urgentJobs() {
		j.urgent.mu.Lock()
		for _, id := range saved[kind] {
			if j.urgent.ids == nil {
				j.urgent.ids = map[string]bool{}
			}
			j.urgent.ids[id] = true
		}
		j.urgent.mu.Unlock()
	}
}

func (l *Library) saveUrgent() {
	urgentFileMu.Lock()
	defer urgentFileMu.Unlock()
	out := map[string][]string{}
	for kind, j := range l.urgentJobs() {
		j.urgent.mu.Lock()
		for id := range j.urgent.ids {
			out[kind] = append(out[kind], id)
		}
		j.urgent.mu.Unlock()
	}
	if len(out) == 0 {
		os.Remove(l.urgentPath())
		return
	}
	if b, err := json.Marshal(out); err == nil {
		writeFileAtomic(l.urgentPath(), b)
	}
}

// markUrgent asks the job of kind for these files now, also outside its window.
func (l *Library) markUrgent(kind string, ids []string) {
	j := l.urgentJobs()[kind]
	if j == nil || (j.client == nil && !j.on) || len(ids) == 0 {
		return
	}
	j.urgent.mu.Lock()
	if j.urgent.ids == nil {
		j.urgent.ids = map[string]bool{}
	}
	for _, id := range ids {
		j.urgent.ids[id] = true
	}
	j.urgent.mu.Unlock()
	l.saveUrgent()
	select { // a job waiting for its window stops waiting for this
	case j.urgentWake <- struct{}{}:
	default:
	}
	j.kick()
}

// moveUrgent lets urgent work follow files that moved.
func (l *Library) moveUrgent(idMap map[string]string) {
	changed := false
	for _, j := range l.urgentJobs() {
		j.urgent.mu.Lock()
		for from, to := range idMap {
			if from != to && j.urgent.ids[from] {
				delete(j.urgent.ids, from)
				j.urgent.ids[to] = true
				changed = true
			}
		}
		j.urgent.mu.Unlock()
	}
	if changed {
		l.saveUrgent()
	}
}

func (j *remoteJob) hasUrgent() bool {
	j.urgent.mu.Lock()
	defer j.urgent.mu.Unlock()
	return len(j.urgent.ids) > 0
}

// urgentTask is t for the urgent files only.
func (j *remoteJob) urgentTask(t jobTask) jobTask {
	j.urgent.mu.Lock()
	want := make(map[string]bool, len(j.urgent.ids))
	for id := range j.urgent.ids {
		want[id] = true
	}
	j.urgent.mu.Unlock()
	u := t
	u.eligible = func(it *Item) bool { return want[it.ID] && t.eligible(it) }
	return u
}

// pruneUrgent forgets urgent files that are done (or gone: only once the
// folders were read, the index from before may miss them).
func (l *Library) pruneUrgent(j *remoteJob, t jobTask) {
	scanned := l.scannedOnce.Load()
	j.urgent.mu.Lock()
	changed := false
	for id := range j.urgent.ids {
		it := l.Get(id)
		if it == nil && !scanned {
			continue
		}
		if it == nil || !t.eligible(it) {
			delete(j.urgent.ids, id)
			changed = true
			continue
		}
		if handled, _ := t.state(it, time.Now()); handled {
			delete(j.urgent.ids, id)
			changed = true
		}
	}
	j.urgent.mu.Unlock()
	if changed {
		l.saveUrgent()
	}
}

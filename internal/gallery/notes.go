package gallery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// Notes are short texts people with an account leave on a photo or video.
// Everybody who can see the item reads them, the search finds them. They
// live in notes.json by root name + "/" + rel and follow moves.

// Note is one note.
type Note struct {
	ID     string `json:"id"`
	User   string `json:"user"`
	Text   string `json:"text"`
	At     int64  `json:"at"`
	Edited int64  `json:"edited,omitempty"`
}

// MaxNote is the longest note in characters.
const MaxNote = 2000

// Errors for the API.
var (
	ErrNoteEmpty    = errors.New("Die Notiz ist leer")
	ErrNoteLong     = errors.New("Die Notiz ist zu lang (höchstens 2000 Zeichen)")
	ErrNoteMissing  = errors.New("Notiz nicht gefunden")
	ErrNoteNotYours = errors.New("Nur eigene Notizen lassen sich ändern")
)

type noteStore struct {
	mu    sync.Mutex
	items map[string][]*Note
	ver   atomic.Int64 // changes with every note (search index)
}

func (l *Library) loadNotes() {
	l.notes.items = map[string][]*Note{}
	b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "notes.json"))
	if err != nil {
		return
	}
	var f struct {
		Items map[string][]*Note `json:"items"`
	}
	if json.Unmarshal(b, &f) == nil && f.Items != nil {
		l.notes.items = f.Items
	}
}

// saveNotes writes notes.json; the caller holds notes.mu.
func (l *Library) saveNotes() error {
	b, err := json.MarshalIndent(map[string]any{"version": 1, "items": l.notes.items}, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(l.opt.CacheDir, "notes.json"), b)
}

// NotesOf returns the notes of an item, oldest first.
func (l *Library) NotesOf(it *Item) []Note {
	l.notes.mu.Lock()
	defer l.notes.mu.Unlock()
	var out []Note
	for _, n := range l.notes.items[l.favKey(it)] {
		out = append(out, *n)
	}
	return out
}

func (l *Library) noteText(it *Item) string {
	l.notes.mu.Lock()
	defer l.notes.mu.Unlock()
	var b strings.Builder
	for _, n := range l.notes.items[l.favKey(it)] {
		b.WriteString(n.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func cleanNote(text string) (string, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return "", ErrNoteEmpty
	}
	if utf8.RuneCountInString(text) > MaxNote {
		return "", ErrNoteLong
	}
	return text, nil
}

func (l *Library) notesChanged() {
	l.notes.ver.Add(1)
	l.bump()
}

// AddNote adds a note by user to an item.
func (l *Library) AddNote(user, id, text string) (Note, error) {
	if user == "" {
		return Note{}, errors.New("Notizen brauchen ein Konto")
	}
	it := l.Get(id)
	if it == nil {
		return Note{}, errors.New("Element nicht gefunden")
	}
	text, err := cleanNote(text)
	if err != nil {
		return Note{}, err
	}
	n := &Note{ID: randomID(), User: user, Text: text, At: nowUnix()}
	l.notes.mu.Lock()
	k := l.favKey(it)
	l.notes.items[k] = append(l.notes.items[k], n)
	err = l.saveNotes()
	l.notes.mu.Unlock()
	l.notesChanged()
	return *n, err
}

// find returns the note and its list; the caller holds notes.mu.
func (l *Library) findNote(itemID, noteID string) (string, int) {
	it := l.Get(itemID)
	if it == nil {
		return "", -1
	}
	k := l.favKey(it)
	for i, n := range l.notes.items[k] {
		if n.ID == noteID {
			return k, i
		}
	}
	return k, -1
}

// UpdateNote changes the text of one's own note.
func (l *Library) UpdateNote(user string, itemID, noteID, text string) (Note, error) {
	text, err := cleanNote(text)
	if err != nil {
		return Note{}, err
	}
	l.notes.mu.Lock()
	k, i := l.findNote(itemID, noteID)
	if i < 0 {
		l.notes.mu.Unlock()
		return Note{}, ErrNoteMissing
	}
	n := l.notes.items[k][i]
	if n.User != user {
		l.notes.mu.Unlock()
		return Note{}, ErrNoteNotYours
	}
	n.Text, n.Edited = text, nowUnix()
	err = l.saveNotes()
	cp := *n
	l.notes.mu.Unlock()
	l.notesChanged()
	return cp, err
}

// DeleteNote removes a note: one's own, or any for admins.
func (l *Library) DeleteNote(user string, admin bool, itemID, noteID string) error {
	l.notes.mu.Lock()
	k, i := l.findNote(itemID, noteID)
	if i < 0 {
		l.notes.mu.Unlock()
		return ErrNoteMissing
	}
	if l.notes.items[k][i].User != user && !admin {
		l.notes.mu.Unlock()
		return ErrNoteNotYours
	}
	list := l.notes.items[k]
	list = append(list[:i:i], list[i+1:]...)
	if len(list) == 0 {
		delete(l.notes.items, k)
	} else {
		l.notes.items[k] = list
	}
	err := l.saveNotes()
	l.notes.mu.Unlock()
	l.notesChanged()
	return err
}

// NoteCount returns how many notes an item has.
func (l *Library) NoteCount(it *Item) int {
	l.notes.mu.Lock()
	defer l.notes.mu.Unlock()
	return len(l.notes.items[l.favKey(it)])
}

// renameNotes moves notes to new keys ("" drops them).
func (l *Library) renameNotes(pairs map[string]string) {
	l.notes.mu.Lock()
	changed := false
	for from, to := range pairs {
		if ns, ok := l.notes.items[from]; ok {
			delete(l.notes.items, from)
			if to != "" {
				l.notes.items[to] = append(l.notes.items[to], ns...)
			}
			changed = true
		}
	}
	if changed {
		if err := l.saveNotes(); err != nil {
			l.logf("notes.json: %v", err)
		}
	}
	l.notes.mu.Unlock()
	if changed {
		l.notesChanged()
	}
}

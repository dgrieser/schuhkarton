// Package auth keeps Schuhkarton's accounts and sessions in users.json next
// to the configuration file.
//
// Roles: admins manage settings, accounts and the photos; users personalise
// (favorites, names of people). Anybody may register an account; it stays
// pending until an admin approves it. Viewing without an account is a
// separate setting (access.anonymous).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Roles.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// SessionTTL is how long a login lasts without use.
const SessionTTL = 90 * 24 * time.Hour

// MinPassword is the shortest accepted password.
const MinPassword = 8

// Errors the API turns into messages.
var (
	ErrBadLogin   = errors.New("Name oder Passwort falsch")
	ErrPending    = errors.New("Das Konto wartet noch auf Freigabe durch einen Admin")
	ErrDisabled   = errors.New("Das Konto ist gesperrt")
	ErrExists     = errors.New("Diesen Namen gibt es schon")
	ErrNotFound   = errors.New("Konto nicht gefunden")
	ErrLastAdmin  = errors.New("Der letzte Admin kann nicht entfernt, gesperrt oder zurückgestuft werden")
	ErrBadName    = errors.New("Name: 1 bis 64 Zeichen, keine Steuerzeichen")
	ErrBadPass    = fmt.Errorf("Passwort: mindestens %d Zeichen", MinPassword)
	ErrNoRegister = errors.New("Registrieren ist ausgeschaltet")
)

// User is one account.
type User struct {
	Name      string    `json:"name"`
	Hash      string    `json:"hash"`
	Role      string    `json:"role"`
	Pending   bool      `json:"pending,omitempty"`  // registered, waiting for an admin
	Disabled  bool      `json:"disabled,omitempty"` // locked by an admin
	Created   time.Time `json:"created"`
	LastLogin time.Time `json:"lastLogin,omitempty"`
}

// Active reports whether the account may sign in.
func (u *User) Active() bool { return u != nil && !u.Pending && !u.Disabled }

// IsAdmin reports an active admin.
func (u *User) IsAdmin() bool { return u.Active() && u.Role == RoleAdmin }

// Info is a user without the password hash, for the API.
type Info struct {
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Pending   bool      `json:"pending,omitempty"`
	Disabled  bool      `json:"disabled,omitempty"`
	Created   time.Time `json:"created"`
	LastLogin time.Time `json:"lastLogin,omitempty"`
	Sessions  int       `json:"sessions"`
}

type session struct {
	Hash    string    `json:"hash"` // sha256 of the cookie value
	User    string    `json:"user"`
	Created time.Time `json:"created"`
	Seen    time.Time `json:"seen"`
}

type file struct {
	Version  int        `json:"version"`
	Users    []*User    `json:"users"`
	Sessions []*session `json:"sessions"`
}

// Store holds accounts and sessions. It is safe for concurrent use and
// picks up changes other processes ("schuhkarton user …") make to the file.
type Store struct {
	path string

	mu       sync.Mutex
	users    map[string]*User // folded name -> user
	sessions map[string]*session
	modTime  time.Time
	checked  time.Time
	dirtyAt  time.Time // a session's Seen changed and is not saved yet

	limiter limiter
}

// PathFor returns where the accounts of a configuration file live.
func PathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "users.json")
}

// Open loads (or starts) the store at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, users: map[string]*User{}, sessions: map[string]*session{}}
	s.limiter.fails = map[string][]time.Time{}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path is the file.
func (s *Store) Path() string { return s.path }

func fold(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// load reads the file; the caller holds mu.
func (s *Store) load() error {
	st, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.users, s.sessions, s.modTime = map[string]*User{}, map[string]*session{}, time.Time{}
		return nil
	}
	if err != nil {
		return err
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	users := map[string]*User{}
	for _, u := range f.Users {
		if u != nil && u.Name != "" {
			users[fold(u.Name)] = u
		}
	}
	sessions := map[string]*session{}
	now := time.Now()
	for _, se := range f.Sessions {
		if se != nil && users[fold(se.User)] != nil && now.Sub(se.Seen) < SessionTTL {
			sessions[se.Hash] = se
		}
	}
	s.users, s.sessions, s.modTime = users, sessions, st.ModTime()
	return nil
}

// refresh reloads the file when another process changed it; the caller
// holds mu.
func (s *Store) refresh() {
	if time.Since(s.checked) < 2*time.Second {
		return
	}
	s.checked = time.Now()
	st, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !s.modTime.IsZero() {
			s.load()
		}
		return
	}
	if !st.ModTime().Equal(s.modTime) {
		if err := s.load(); err != nil {
			// keep what we have; the file is probably being written
			return
		}
	}
}

// save writes the file (0600: it holds password hashes); the caller holds mu.
func (s *Store) save() error {
	f := file{Version: 1}
	for _, u := range s.users {
		f.Users = append(f.Users, u)
	}
	sort.Slice(f.Users, func(a, b int) bool { return fold(f.Users[a].Name) < fold(f.Users[b].Name) })
	for _, se := range s.sessions {
		f.Sessions = append(f.Sessions, se)
	}
	sort.Slice(f.Sessions, func(a, b int) bool { return f.Sessions[a].Created.Before(f.Sessions[b].Created) })
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if st, err := os.Stat(s.path); err == nil {
		s.modTime = st.ModTime()
	}
	s.dirtyAt = time.Time{}
	return nil
}

// --- accounts -------------------------------------------------------------------

// CheckName validates a user name: any printable text up to 64 characters.
func CheckName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 || !utf8.ValidString(name) {
		return ErrBadName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return ErrBadName
		}
	}
	return nil
}

// CheckPassword validates a new password.
func CheckPassword(pw string) error {
	if n := utf8.RuneCountInString(pw); n < MinPassword || n > 256 {
		return ErrBadPass
	}
	return nil
}

// Count returns the number of accounts and of active admins.
func (s *Store) Count() (users, admins int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	for _, u := range s.users {
		users++
		if u.IsAdmin() {
			admins++
		}
	}
	return
}

// HasAdmin reports whether an active admin exists.
func (s *Store) HasAdmin() bool {
	_, a := s.Count()
	return a > 0
}

// Pending returns the number of accounts waiting for approval.
func (s *Store) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	n := 0
	for _, u := range s.users {
		if u.Pending {
			n++
		}
	}
	return n
}

// FirstAdmin returns the name of the oldest active admin ("" if none).
func (s *Store) FirstAdmin() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	var first *User
	for _, u := range s.users {
		if u.IsAdmin() && (first == nil || u.Created.Before(first.Created)) {
			first = u
		}
	}
	if first == nil {
		return ""
	}
	return first.Name
}

// List returns all accounts, admins first, then by name.
func (s *Store) List() []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	count := map[string]int{}
	for _, se := range s.sessions {
		count[fold(se.User)]++
	}
	out := make([]Info, 0, len(s.users))
	for k, u := range s.users {
		out = append(out, Info{Name: u.Name, Role: u.Role, Pending: u.Pending, Disabled: u.Disabled,
			Created: u.Created, LastLogin: u.LastLogin, Sessions: count[k]})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Pending != out[b].Pending {
			return out[a].Pending
		}
		if (out[a].Role == RoleAdmin) != (out[b].Role == RoleAdmin) {
			return out[a].Role == RoleAdmin
		}
		return fold(out[a].Name) < fold(out[b].Name)
	})
	return out
}

// Get returns a copy of one account.
func (s *Store) Get(name string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	u := s.users[fold(name)]
	if u == nil {
		return User{}, false
	}
	return *u, true
}

// Create adds an account. pending accounts need an admin's approval.
func (s *Store) Create(name, password, role string, pending bool) error {
	name = strings.TrimSpace(name)
	if err := CheckName(name); err != nil {
		return err
	}
	if err := CheckPassword(password); err != nil {
		return err
	}
	if role != RoleAdmin {
		role = RoleUser
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	if s.users[fold(name)] != nil {
		return ErrExists
	}
	s.users[fold(name)] = &User{Name: name, Hash: hash, Role: role, Pending: pending, Created: time.Now().UTC()}
	return s.save()
}

// Update changes an account. Nil fields stay as they are. The last active
// admin cannot lose that status.
type Update struct {
	Role     *string `json:"role,omitempty"`
	Approve  bool    `json:"approve,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
	Password *string `json:"password,omitempty"`
}

// Apply applies an Update.
func (s *Store) Apply(name string, up Update) error {
	var hash string
	if up.Password != nil {
		if err := CheckPassword(*up.Password); err != nil {
			return err
		}
		h, err := HashPassword(*up.Password)
		if err != nil {
			return err
		}
		hash = h
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	u := s.users[fold(name)]
	if u == nil {
		return ErrNotFound
	}
	next := *u
	if up.Role != nil {
		next.Role = RoleUser
		if *up.Role == RoleAdmin {
			next.Role = RoleAdmin
		}
	}
	if up.Approve {
		next.Pending = false
	}
	if up.Disabled != nil {
		next.Disabled = *up.Disabled
	}
	if u.IsAdmin() && !next.IsAdmin() && s.activeAdmins() <= 1 {
		return ErrLastAdmin
	}
	if hash != "" {
		next.Hash = hash
	}
	*u = next
	if hash != "" || u.Disabled {
		s.dropSessions(u.Name, "")
	}
	return s.save()
}

// Delete removes an account and its sessions.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	u := s.users[fold(name)]
	if u == nil {
		return ErrNotFound
	}
	if u.IsAdmin() && s.activeAdmins() <= 1 {
		return ErrLastAdmin
	}
	delete(s.users, fold(name))
	s.dropSessions(u.Name, "")
	return s.save()
}

func (s *Store) activeAdmins() int {
	n := 0
	for _, u := range s.users {
		if u.IsAdmin() {
			n++
		}
	}
	return n
}

// ChangePassword lets a user replace their own password; other sessions of
// the account end, keep (the cookie value of the current one) stays.
func (s *Store) ChangePassword(name, old, pw, keep string) error {
	if err := CheckPassword(pw); err != nil {
		return err
	}
	s.mu.Lock()
	u := s.users[fold(name)]
	var cur string
	if u != nil {
		cur = u.Hash
	}
	s.mu.Unlock()
	if u == nil {
		return ErrNotFound
	}
	if ok := VerifyPassword(cur, old); !ok {
		return ErrBadLogin
	}
	hash, err := HashPassword(pw)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u = s.users[fold(name)]
	if u == nil {
		return ErrNotFound
	}
	u.Hash = hash
	s.dropSessions(u.Name, hashToken(keep))
	return s.save()
}

// ClaimAdmin makes an existing account an active admin once its password is
// confirmed. The setup link uses it while there is no admin yet, for
// somebody who registered at /login before opening the link.
func (s *Store) ClaimAdmin(name, password string) error {
	s.mu.Lock()
	s.refresh()
	u := s.users[fold(name)]
	hash := dummyHash
	if u != nil {
		hash = u.Hash
	}
	s.mu.Unlock()
	if ok := VerifyPassword(hash, password); !ok || u == nil {
		return ErrBadLogin
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u = s.users[fold(name)]
	if u == nil {
		return ErrBadLogin
	}
	u.Role, u.Pending, u.Disabled = RoleAdmin, false, false
	return s.save()
}

// --- sessions -------------------------------------------------------------------

// Login checks name and password and starts a session. It returns the
// cookie value. client identifies the caller for the rate limit (IP).
func (s *Store) Login(name, password, client string) (string, *User, error) {
	client = clientKey(client)
	keys := []string{"ip:" + client, "ipu:" + client + "|" + fold(name)}
	// the attempt counts before the password is checked: attempts arriving
	// at the same time would otherwise all get past the limit
	if wait := s.limiter.attempt(keys...); wait > 0 {
		return "", nil, tooMany(wait)
	}
	s.mu.Lock()
	s.refresh()
	u := s.users[fold(name)]
	hash := dummyHash
	if u != nil {
		hash = u.Hash
	}
	s.mu.Unlock()
	ok := VerifyPassword(hash, password) // also for unknown names: same timing
	if !ok || u == nil {
		return "", nil, ErrBadLogin
	}
	s.limiter.forgive(keys[0]) // not a failed attempt after all
	s.limiter.reset(keys[1])
	if u.Pending {
		return "", nil, ErrPending
	}
	if u.Disabled {
		return "", nil, ErrDisabled
	}
	tok := newToken()
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	u = s.users[fold(name)]
	if u == nil {
		return "", nil, ErrBadLogin
	}
	u.LastLogin = now
	s.sessions[hashToken(tok)] = &session{Hash: hashToken(tok), User: u.Name, Created: now, Seen: now}
	if err := s.save(); err != nil {
		return "", nil, err
	}
	cp := *u
	return tok, &cp, nil
}

// StartSession signs a user in without a password (right after the setup
// wizard created the admin).
func (s *Store) StartSession(name string) (string, error) {
	tok := newToken()
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[fold(name)]
	if !u.Active() {
		return "", ErrNotFound
	}
	u.LastLogin = now
	s.sessions[hashToken(tok)] = &session{Hash: hashToken(tok), User: u.Name, Created: now, Seen: now}
	return tok, s.save()
}

// Session returns the account of a cookie value, or nil.
func (s *Store) Session(tok string) *User {
	if tok == "" {
		return nil
	}
	h := hashToken(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	se := s.sessions[h]
	if se == nil {
		return nil
	}
	now := time.Now()
	if now.Sub(se.Seen) > SessionTTL {
		delete(s.sessions, h)
		s.save()
		return nil
	}
	u := s.users[fold(se.User)]
	if !u.Active() {
		return nil
	}
	if now.Sub(se.Seen) > time.Hour {
		se.Seen = now.UTC()
		if s.dirtyAt.IsZero() {
			s.dirtyAt = now
		}
	}
	if !s.dirtyAt.IsZero() && now.Sub(s.dirtyAt) > 10*time.Minute {
		s.save()
	}
	cp := *u
	return &cp
}

// Logout ends one session.
func (s *Store) Logout(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[hashToken(tok)]; ok {
		delete(s.sessions, hashToken(tok))
		s.save()
	}
}

// Flush writes pending session times.
func (s *Store) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirtyAt.IsZero() {
		s.save()
	}
}

// dropSessions ends all sessions of a user except keep; the caller holds mu.
func (s *Store) dropSessions(name, keep string) {
	for h, se := range s.sessions {
		if fold(se.User) == fold(name) && h != keep {
			delete(s.sessions, h)
		}
	}
}

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// --- passwords ------------------------------------------------------------------

// argon2id parameters (OWASP minimum: 19 MiB, 2 passes): light enough for a
// Raspberry Pi. They are stored in the hash.
const (
	argonMemory  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
)

// hashSlots limits how many passwords are hashed at the same time. Every
// hash takes argonMemory, and anybody can ask for one by trying to sign in:
// without a limit a burst of attempts uses up the machine's memory.
var hashSlots = make(chan struct{}, min(4, max(2, runtime.NumCPU())))

func argonKey(pw string, salt []byte, t, m uint32, p uint8, n uint32) []byte {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return argon2.IDKey([]byte(pw), salt, t, m, p, n)
}

// HashPassword returns an argon2id PHC string.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argonKey(pw, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks pw against a PHC hash in constant time.
func VerifyPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 || m > 1<<20 || t > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	if len(want) > 1024 {
		return false
	}
	got := argonKey(pw, salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var dummyHash = func() string {
	h, _ := HashPassword("schuhkarton-timing")
	return h
}()

// --- rate limit -----------------------------------------------------------------

// limiter blocks a client (IP), or a client for one name, after 8 failed
// logins within 15 minutes. Names alone are not blocked: that would let
// anybody lock the admin out.
type limiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
	swept time.Time
}

func tooMany(wait time.Duration) error {
	return fmt.Errorf("Zu viele Versuche, bitte in %d Minuten noch einmal", int(wait.Minutes())+1)
}

// clientKey is what the limit counts a client by: its address, for IPv6 the
// /64 network (one connection usually has all of it to choose from).
func clientKey(client string) string {
	ip, err := netip.ParseAddr(client)
	if err != nil {
		return client
	}
	ip = ip.Unmap().WithZone("")
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}

// AllowRegister counts a registration for a client and reports whether it
// may go on: a client gets limitMax of them in limitWindow.
func (s *Store) AllowRegister(client string) error {
	if wait := s.limiter.attempt("reg:" + clientKey(client)); wait > 0 {
		return tooMany(wait)
	}
	return nil
}

const (
	limitWindow = 15 * time.Minute
	limitMax    = 8 // per client and name; a client gets 4x as many overall
)

func (l *limiter) recent(k string, now time.Time) []time.Time {
	var out []time.Time
	for _, t := range l.fails[k] {
		if now.Sub(t) < limitWindow {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		delete(l.fails, k)
	} else {
		l.fails[k] = out
	}
	return out
}

// wait returns how long the keys are blocked; the caller holds mu.
func (l *limiter) wait(now time.Time, keys ...string) time.Duration {
	var wait time.Duration
	for _, k := range keys {
		n := limitMax
		if strings.HasPrefix(k, "ip:") {
			n = 4 * limitMax
		}
		if r := l.recent("k:"+k, now); len(r) >= n {
			wait = max(wait, limitWindow-now.Sub(r[len(r)-n]))
		}
	}
	return wait
}

// attempt counts an attempt for the keys unless one of them is blocked
// (then it returns how long), in one step.
func (l *limiter) attempt(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if wait := l.wait(now, keys...); wait > 0 {
		return wait
	}
	for _, k := range keys {
		l.fails["k:"+k] = append(l.recent("k:"+k, now), now)
	}
	l.sweep(now)
	return 0
}

// forgive takes the latest attempt of the keys back.
func (l *limiter) forgive(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if f := l.fails["k:"+k]; len(f) > 1 {
			l.fails["k:"+k] = f[:len(f)-1]
		} else {
			delete(l.fails, "k:"+k)
		}
	}
}

// sweep forgets keys without recent attempts, now and then; the caller
// holds mu. Without it a key lives until it is asked for again.
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < limitWindow {
		return
	}
	l.swept = now
	for k, f := range l.fails {
		if len(f) == 0 || now.Sub(f[len(f)-1]) >= limitWindow {
			delete(l.fails, k)
		}
	}
}

func (l *limiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.fails, "k:"+k)
	}
}

// Package app joins the two demonstrators behind one HTTP server, one login
// and one port.
//
// Each signed-in session gets its own instance of both applications, so two
// people using the deployment at the same time never see each other's
// documents, tables or issuance logs. All state is still in memory and is lost
// when the process restarts or the session expires.
package app

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"attribution/common/codec"
	"attribution/common/store"
	"attribution/common/webapp"
	custodial "attribution/custodial/server"
	"attribution/internal/audit"
	provenance "attribution/provenance/server"
)

const (
	cookieName  = "custodial_session"
	sessionTTL  = 8 * time.Hour
	idleTTL     = 2 * time.Hour
	maxSessions = 200
)

// Session is one signed-in browser, with a private copy of both applications.
type Session struct {
	id       string
	user     string // the email address that signed in
	created  time.Time
	lastSeen time.Time
	doc      http.Handler
	data     http.Handler
}

type Config struct {
	Users  Users  // who may sign in, from config.yaml
	Master string // password for /debug/log
	// Secret derives each user's marking key, so copies issued before a
	// restart can still be read afterwards. Changing it invalidates them all.
	Secret        string
	Store         *store.Store
	CustodialWeb  string
	ProvenanceWeb string
	SharedWeb     string
	SecureCookie  bool
}

type App struct {
	cfg Config
	log *audit.Log

	mu       sync.Mutex
	sessions map[string]*Session
	fails    map[string]int // failed logins per client, to slow guessing
}

func New(cfg Config) *App {
	a := &App{cfg: cfg, log: audit.New(os.Stderr), sessions: map[string]*Session{}, fails: map[string]int{}}
	go a.sweep()
	return a
}

// sweep drops sessions that have expired or gone idle, so memory does not grow
// without bound.
func (a *App) sweep() {
	for range time.Tick(5 * time.Minute) {
		now := time.Now()
		a.mu.Lock()
		for id, s := range a.sessions {
			if now.Sub(s.created) > sessionTTL || now.Sub(s.lastSeen) > idleTTL {
				delete(a.sessions, id)
			}
		}
		a.mu.Unlock()
	}
}

func newID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// session returns the caller's session, or nil when they are not signed in.
func (a *App) session(r *http.Request) *Session {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[c.Value]
	if s == nil {
		return nil
	}
	now := time.Now()
	if now.Sub(s.created) > sessionTTL || now.Sub(s.lastSeen) > idleTTL {
		delete(a.sessions, c.Value)
		return nil
	}
	s.lastSeen = now
	// Both applications are built on first use, because each one typesets a
	// sample document and generates a sample table.
	key := codec.Key(store.KeyFor(a.cfg.Secret, s.user))
	if s.doc == nil {
		s.doc = custodial.New(key, s.user, a.cfg.Store).Handler(os.DirFS(a.cfg.CustodialWeb), "")
	}
	if s.data == nil {
		s.data = provenance.New(key, s.user, a.cfg.Store).Handler(os.DirFS(a.cfg.ProvenanceWeb), "")
	}
	return s
}

func (a *App) create(w http.ResponseWriter, user string) {
	id := newID()
	a.mu.Lock()
	if len(a.sessions) >= maxSessions {
		for k := range a.sessions { // drop an arbitrary one rather than refuse
			delete(a.sessions, k)
			break
		}
	}
	a.sessions[id] = &Session{id: id, user: user, created: time.Now(), lastSeen: time.Now()}
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (a *App) destroy(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		if s := a.sessions[c.Value]; s != nil {
			a.log.Add(audit.Entry{User: s.user, Client: client(r), Action: "signed out"})
		}
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

// client identifies the caller for login throttling.
func client(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	host, _, _ := strings.Cut(r.RemoteAddr, ":")
	return host
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	who := client(r)

	a.mu.Lock()
	n := a.fails[who]
	a.mu.Unlock()
	if n > 0 {
		// A short, growing pause. Enough to make guessing tedious without
		// locking anyone out of a demonstration.
		time.Sleep(min(time.Duration(n)*300*time.Millisecond, 5*time.Second))
	}

	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	given := r.PostFormValue("password")

	// Compare against a stored password whether or not the address exists, so
	// an unknown address is not distinguishable from a wrong password.
	want, known := a.cfg.Users[email]
	ok := subtle.ConstantTimeCompare([]byte(given), []byte(want)) == 1
	if !known || !ok {
		a.mu.Lock()
		a.fails[who] = n + 1
		a.mu.Unlock()
		a.log.Add(audit.Entry{User: email, Client: who, Action: "sign-in refused"})
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	a.mu.Lock()
	delete(a.fails, who)
	a.mu.Unlock()
	a.log.Add(audit.Entry{User: email, Client: who, Action: "signed in"})
	a.create(w, email)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// requirePage sends a signed-out browser to the login screen.
func (a *App) requirePage(next func(http.ResponseWriter, *http.Request, *Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := a.session(r)
		if s == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r, s)
	}
}

// requireAPI answers a signed-out API caller with JSON, so the frontend can
// show the message rather than rendering a login page into a fetch.
func (a *App) requireAPI(prefix string, pick func(*Session) http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := a.session(r)
		if s == nil {
			webapp.WriteErr(w, http.StatusUnauthorized, "signed out, reload the page to sign in again")
			return
		}
		// The applications were written to serve /api/... directly, so the
		// product prefix is stripped before handing the request over.
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/api" + strings.TrimPrefix(r.URL.Path, prefix)
		pick(s).ServeHTTP(w, r2)
	}
}

func staticDir(dir string) http.Handler {
	return webapp.NoCache(http.FileServer(http.Dir(dir)))
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	// The stylesheet and wordmark are needed by the login page itself.
	mux.Handle("GET /shared/", http.StripPrefix("/shared/", staticDir(a.cfg.SharedWeb)))

	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		if a.session(r) != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		http.ServeFile(w, r, path.Join(a.cfg.SharedWeb, "login.html"))
	})
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) {
		a.destroy(w, r)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})

	custodialFiles := http.StripPrefix("/custodial/", staticDir(a.cfg.CustodialWeb))
	provenanceFiles := http.StripPrefix("/provenance/", staticDir(a.cfg.ProvenanceWeb))
	mux.Handle("GET /custodial/", a.requirePage(func(w http.ResponseWriter, r *http.Request, _ *Session) {
		custodialFiles.ServeHTTP(w, r)
	}))
	mux.Handle("GET /provenance/", a.requirePage(func(w http.ResponseWriter, r *http.Request, _ *Session) {
		provenanceFiles.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		s := a.session(r)
		if s == nil {
			webapp.WriteErr(w, http.StatusUnauthorized, "signed out")
			return
		}
		webapp.WriteJSON(w, map[string]any{"user": s.user})
	})

	who := func(r *http.Request) string {
		if s := a.session(r); s != nil {
			return s.user
		}
		return ""
	}
	mux.Handle("/api/doc/", a.record(who, a.requireAPI("/api/doc", func(s *Session) http.Handler { return s.doc })))
	mux.Handle("/api/data/", a.record(who, a.requireAPI("/api/data", func(s *Session) http.Handler { return s.data })))
	mux.Handle("GET /debug/log", http.HandlerFunc(a.debugLog))

	mux.Handle("GET /{$}", a.requirePage(func(w http.ResponseWriter, r *http.Request, _ *Session) {
		http.ServeFile(w, r, path.Join(a.cfg.SharedWeb, "home.html"))
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	return mux
}

// debugLog hands over the whole record to whoever holds the master password.
// It is deliberately separate from the user list: signing in as a user is not
// enough to read what everyone else did.
func (a *App) debugLog(w http.ResponseWriter, r *http.Request) {
	_, given, ok := r.BasicAuth()
	if !ok || a.cfg.Master == "" || subtle.ConstantTimeCompare([]byte(given), []byte(a.cfg.Master)) != 1 {
		a.log.Add(audit.Entry{Client: client(r), Action: "log access refused"})
		w.Header().Set("WWW-Authenticate", `Basic realm="log"`)
		http.Error(w, "the master password is required", http.StatusUnauthorized)
		return
	}
	all := r.URL.Query().Get("all") == "1"
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		a.log.WriteJSON(w, all)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	a.log.WriteText(w, all)
}

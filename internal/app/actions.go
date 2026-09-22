package app

import (
	"net/http"
	"strings"
	"time"

	"attribution/internal/audit"
)

// actions turns an API path into something readable, so the record says what
// somebody did rather than which endpoint they hit. Anything marked routine is
// polling or a plain read, and is hidden from the log unless asked for.
var actions = map[string]struct {
	name    string
	routine bool
}{
	"POST /api/doc/source":    {"loaded a document", false},
	"POST /api/doc/issue":     {"issued marked copies", false},
	"GET /api/doc/copy":       {"downloaded a copy", false},
	"GET /api/doc/bundle":     {"downloaded all copies", false},
	"POST /api/doc/detect":    {"verified a file", false},
	"GET /api/doc/master.pdf": {"previewed the document", false},
	"GET /api/doc/state":      {"read document state", true},

	"POST /api/data/source":    {"loaded a table", false},
	"POST /api/data/schema":    {"confirmed column roles", false},
	"POST /api/data/preview":   {"changed the marking measures", true},
	"GET /api/data/marked.csv": {"downloaded a marked copy", false},
	"GET /api/data/source.csv": {"downloaded the source table", false},
	"POST /api/data/issue":     {"issued marked copies", false},
	"POST /api/data/detect":    {"verified a file", false},
	"GET /api/data/state":      {"read table state", true},

	"GET /api/session": {"checked the session", true},
}

func describe(r *http.Request) (name string, routine bool) {
	if a, ok := actions[r.Method+" "+r.URL.Path]; ok {
		return a.name, a.routine
	}
	return strings.ToLower(r.Method) + " " + r.URL.Path, false
}

func product(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/doc/"):
		return "custodial"
	case strings.HasPrefix(path, "/api/data/"):
		return "provenance"
	}
	return ""
}

// status remembers what was written, so the record can show failures.
type status struct {
	http.ResponseWriter
	code int
}

func (s *status) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

func (s *status) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// record wraps an API handler so every call lands in the audit log, with the
// signed-in user attached.
func (a *App) record(user func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &status{ResponseWriter: w}
		next.ServeHTTP(sw, r)

		name, routine := describe(r)
		e := audit.Entry{
			User:    user(r),
			Client:  client(r),
			Product: product(r.URL.Path),
			Action:  name,
			Status:  sw.code,
			Millis:  time.Since(start).Milliseconds(),
			Routine: routine,
		}
		if f := r.URL.Query().Get("mark"); f != "" {
			e.Detail = "mark " + f
		}
		a.log.Add(e)
	})
}

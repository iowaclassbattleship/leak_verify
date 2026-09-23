// Package server exposes Provenance as a local JSON API and serves the
// frontend. All state is in memory; nothing leaves the host.
package server

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sync"

	"attribution/common/codec"
	"attribution/common/store"
	"attribution/common/webapp"
	"attribution/provenance/internal/tabular"
)

type Server struct {
	user   string
	store  *store.Store
	logged []loggedIssuance

	mu  sync.Mutex
	key codec.Key
	tab *tabState
	// tables holds every table loaded in this session by source, so Verify
	// can check a file against the copies of any of them, not only the one
	// on the Mark tab. The sample comes back identical on every load.
	tables map[string]*tabular.Table
}

// New builds one user's instance. key is derived per user so it survives a
// restart, and st holds their issuance log between processes.
func New(key codec.Key, user string, st *store.Store) *Server {
	s := &Server{key: key, user: user, store: st, tables: map[string]*tabular.Table{}}
	s.loadLog()
	s.reset(s.sampleTable(), sampleTableSource, "accounts")
	return s
}

// sampleTable is seeded from the user's key rather than the clock, so it is
// the same table every time it is loaded, and copies issued from it verify
// after a restart.
func (s *Server) sampleTable() *tabular.Table {
	return tabular.Generate(2000, binary.BigEndian.Uint64(s.key.Sum("sample-table")))
}

func (s *Server) Handler(web fs.FS, shared string) http.Handler {
	mux := http.NewServeMux()
	webapp.Shared(mux, shared)
	// Revalidate on every request so edited frontend files show up on refresh.
	mux.Handle("GET /", webapp.NoCache(http.FileServerFS(web)))

	mux.HandleFunc("GET /api/state", s.stateHandler)
	mux.HandleFunc("POST /api/source", s.source)
	mux.HandleFunc("POST /api/schema", s.schema)
	mux.HandleFunc("POST /api/preview", s.preview)
	mux.HandleFunc("GET /api/marked.csv", s.markedCSV)
	mux.HandleFunc("GET /api/source.csv", s.sourceCSV)
	mux.HandleFunc("POST /api/issue", s.issue)
	mux.HandleFunc("GET /api/copy", s.copy)
	mux.HandleFunc("POST /api/recall", s.recall)
	mux.HandleFunc("GET /api/bundle", s.bundle)
	mux.HandleFunc("POST /api/detect", s.detect)
	mux.HandleFunc("POST /api/leak", s.leak)
	mux.HandleFunc("GET /api/leak", s.leakCSV)
	mux.HandleFunc("POST /api/matrix", s.matrix)
	// Requests are recorded by the audit log in internal/app.
	return mux
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// fileStem turns free text into a conservative file-name fragment.
func fileStem(text string) string { return webapp.FileStem(text, "table") }

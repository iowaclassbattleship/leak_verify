// Package server exposes Provenance as a local JSON API and serves the
// frontend. All state is in memory; nothing leaves the host.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"attribution/common/codec"
	"attribution/common/webapp"
	"attribution/provenance/internal/tabular"
)

type Server struct {
	mu  sync.Mutex
	key codec.Key
	tab *tabState
}

func New() *Server {
	s := &Server{key: codec.NewKey()}
	s.reset(tabular.Generate(2000, uint64(time.Now().UnixNano())), sampleTableSource, "accounts")
	return s
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
	mux.HandleFunc("GET /api/bundle", s.bundle)
	mux.HandleFunc("POST /api/detect", s.detect)
	mux.HandleFunc("POST /api/leak", s.leak)
	mux.HandleFunc("GET /api/leak", s.leakCSV)
	mux.HandleFunc("POST /api/matrix", s.matrix)
	return webapp.LogRequests(mux)
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// fileStem turns free text into a conservative file-name fragment.
func fileStem(text string) string { return webapp.FileStem(text, "table") }

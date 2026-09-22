// Package server exposes both attribution modules as a local JSON API and
// serves the static frontend. All state is in memory; nothing leaves the host.
package server

import (
	"io/fs"
	"net/http"
	"sync"

	"attribution/common/codec"
	"attribution/common/store"
	"attribution/common/webapp"
	"attribution/custodial/internal/document"
)

type Server struct {
	user  string
	store *store.Store

	mu     sync.Mutex
	key    codec.Key
	engine *document.Engine
	doc    *docState
}

// New builds one user's instance. key is derived per user so it survives a
// restart, and st holds their issuance log between processes.
func New(key codec.Key, user string, st *store.Store) *Server {
	s := &Server{key: key, engine: document.NewEngine(key), user: user, store: st}
	s.resetDocument(s.engine.NewMaster(document.SampleDoc(), sampleSource), "Project-Halcyon-Board-Briefing")
	s.loadLog()
	return s
}

func (s *Server) Handler(web fs.FS, shared string) http.Handler {
	mux := http.NewServeMux()
	webapp.Shared(mux, shared)
	// Revalidate on every request so edited frontend files show up on refresh.
	mux.Handle("GET /", webapp.NoCache(http.FileServerFS(web)))

	mux.HandleFunc("GET /api/state", s.docStateHandler)
	mux.HandleFunc("POST /api/source", s.docSource)
	mux.HandleFunc("GET /api/master.pdf", s.docMasterPDF)
	mux.HandleFunc("POST /api/issue", s.docIssue)
	mux.HandleFunc("GET /api/copy", s.docCopy)
	mux.HandleFunc("POST /api/recall", s.docRecall)
	mux.HandleFunc("GET /api/bundle", s.docBundle)
	mux.HandleFunc("POST /api/detect", s.docDetect)
	// Requests are recorded by the audit log in internal/app.
	return mux
}

// Package server exposes both attribution modules as a local JSON API and
// serves the static frontend. All state is in memory; nothing leaves the host.
package server

import (
	"io/fs"
	"net/http"
	"sync"

	"attribution/common/codec"
	"attribution/common/webapp"
	"attribution/custodial/internal/document"
)

type Server struct {
	mu     sync.Mutex
	key    codec.Key
	engine *document.Engine
	doc    *docState
}

func New() *Server {
	key := codec.NewKey()
	s := &Server{key: key, engine: document.NewEngine(key)}
	s.resetDocument(s.engine.NewMaster(document.SampleDoc(), sampleSource), "Project-Halcyon-Board-Briefing")
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
	mux.HandleFunc("GET /api/bundle", s.docBundle)
	mux.HandleFunc("POST /api/detect", s.docDetect)
	return webapp.LogRequests(mux)
}

// Package server exposes both attribution modules as a local JSON API and
// serves the static frontend. All state is in memory; nothing leaves the host.
package server

import (
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"sync"

	"custodial/internal/codec"
	"custodial/internal/document"
)

const maxUpload = 25 << 20

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

func (s *Server) Handler(web fs.FS) http.Handler {
	mux := http.NewServeMux()
	// Revalidate on every request so edited frontend files show up on refresh.
	mux.Handle("GET /", noCache(http.FileServerFS(web)))

	mux.HandleFunc("GET /api/state", s.docStateHandler)
	mux.HandleFunc("POST /api/source", s.docSource)
	mux.HandleFunc("GET /api/master.pdf", s.docMasterPDF)
	mux.HandleFunc("POST /api/issue", s.docIssue)
	mux.HandleFunc("GET /api/copy", s.docCopy)
	mux.HandleFunc("GET /api/bundle", s.docBundle)
	mux.HandleFunc("POST /api/detect", s.docDetect)
	return logRequests(mux)
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && len(r.URL.Path) > 4 && r.URL.Path[:5] == "/api/" {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// readUpload returns the bytes and name of the multipart field "file".
func readUpload(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected a file upload (max 25 MB)")
		return nil, "", false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, "", false
	}
	return data, hdr.Filename, true
}

func sendFile(w http.ResponseWriter, contentType, name string, data []byte, inline bool) {
	disp := "attachment"
	if inline {
		disp = "inline"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disp+`; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

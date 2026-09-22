// Package webapp holds the plumbing both demonstrators share: the JSON and
// upload helpers their APIs are built from, and the static file serving that
// lets a frontend file be edited and reloaded without restarting the server.
package webapp

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxUpload caps a recovered artifact. Everything is held in memory.
const MaxUpload = 25 << 20

// NoCache makes the browser revalidate, so an edited frontend file shows up on
// a refresh rather than being served from the disk cache.
func NoCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

// LogRequests logs API calls only, so the static files stay out of the way.
func LogRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}

func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func WriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func ReadJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		WriteErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// ReadUpload returns the bytes and name of the multipart field "file".
func ReadUpload(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		WriteErr(w, http.StatusBadRequest, "expected a file upload (max 25 MB)")
		return nil, "", false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return nil, "", false
	}
	return data, hdr.Filename, true
}

// SendFile returns one artifact, as a download unless inline is set.
func SendFile(w http.ResponseWriter, contentType, name string, data []byte, inline bool) {
	disp := "attachment"
	if inline {
		disp = "inline"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disp+`; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// FileStem turns free text into a conservative file-name fragment.
func FileStem(text, fallback string) string {
	stem := strings.Trim(unsafeFileChars.ReplaceAllString(text, "-"), "-.")
	if len(stem) > 60 {
		stem = strings.TrimRight(stem[:60], "-.")
	}
	if stem == "" {
		return fallback
	}
	return stem
}

// Shared mounts the house stylesheet and logo at /shared/, so both
// applications look like one product without duplicating the files.
func Shared(mux *http.ServeMux, dir string) {
	if dir == "" {
		return
	}
	mux.Handle("GET /shared/", NoCache(http.StripPrefix("/shared/", http.FileServer(http.Dir(dir)))))
}

// SharedDir finds the house stylesheet, wherever the app was started from.
func SharedDir(candidates ...string) string {
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "base.css")); err == nil {
			return dir
		}
	}
	return ""
}

// WebDir returns the first candidate directory that holds an index.html, so
// each app runs both from the repository root and from its own directory.
func WebDir(candidates ...string) (string, error) {
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("no frontend found in %s (run from the repository root, or pass -root)", strings.Join(candidates, " or "))
}

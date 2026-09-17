// Command custodial runs the document attribution demonstrator: a local web
// server that marks a PDF per recipient and identifies a recovered copy.
//
// The frontend is served straight from the web/ directory, so editing an HTML,
// CSS or JS file only needs a browser refresh, not a restart.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"custodial/internal/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address (keep it local: this is an on-prem demonstrator)")
	webDir := flag.String("web", "web", "directory holding the frontend files")
	flag.Parse()

	if _, err := os.Stat(filepath.Join(*webDir, "index.html")); err != nil {
		log.Fatalf("no frontend at %s (run from the project directory, or pass -web): %v", *webDir, err)
	}
	srv := server.New()
	log.Printf("Custodial listening on http://%s (serving %s)", *addr, *webDir)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler(os.DirFS(*webDir))))
}

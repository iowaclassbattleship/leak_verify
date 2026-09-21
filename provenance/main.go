// Command provenance runs the dataset attribution demonstrator: a local web
// server that marks a table per recipient and identifies a recovered copy.
//
// The frontend is served straight from the web/ directory, so editing an HTML,
// CSS or JS file only needs a browser refresh, not a restart.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"attribution/common/webapp"
	"attribution/provenance/internal/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8081", "listen address (keep it local: this is an on-prem demonstrator)")
	webDir := flag.String("web", "", "directory holding the frontend files")
	flag.Parse()

	shared := webapp.SharedDir("common/web", "../common/web")
	dir, err := webapp.WebDir(*webDir, "provenance/web", "web")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Provenance listening on http://%s (serving %s)", *addr, dir)
	log.Fatal(http.ListenAndServe(*addr, server.New().Handler(os.DirFS(dir), shared)))
}

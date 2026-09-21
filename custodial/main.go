// Command custodial runs the document attribution demonstrator: a local web
// server that marks a document per recipient and identifies a recovered copy.
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
	"attribution/custodial/internal/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address (keep it local: this is an on-prem demonstrator)")
	webDir := flag.String("web", "", "directory holding the frontend files")
	flag.Parse()

	dir, err := webapp.WebDir(*webDir, "custodial/web", "web")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Custodial listening on http://%s (serving %s)", *addr, dir)
	log.Fatal(http.ListenAndServe(*addr, server.New().Handler(os.DirFS(dir))))
}

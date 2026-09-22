// Command custodial serves both demonstrators, Custodial for documents and
// Provenance for datasets, from one process behind a single login.
//
// Everything is held in memory, per signed-in session, and is lost when the
// process restarts. This is a demonstrator, not a production service.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"attribution/common/store"
	"attribution/common/webapp"
	"attribution/internal/app"
)

func main() {
	addr := flag.String("addr", envOr("ADDR", "127.0.0.1:8080"), "listen address")
	configPath := flag.String("config", envOr("CONFIG", "config.yaml"), "path to the user list")
	secure := flag.Bool("secure-cookie", os.Getenv("SECURE_COOKIE") == "1", "set the Secure flag on the session cookie (use behind HTTPS)")
	root := flag.String("root", envOr("ROOT", "."), "directory holding custodial/web, provenance/web and common/web")
	dataDir := flag.String("data", envOr("DATA_DIR", "data"), "where issuance logs are kept; empty keeps nothing")
	flag.Parse()

	settings, err := app.LoadUsers(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d users loaded from %s", len(settings.Users), *configPath)

	cfg := app.Config{
		Users:         settings.Users,
		Master:        settings.Master,
		Secret:        settings.Secret,
		Store:         &store.Store{Dir: *dataDir},
		CustodialWeb:  mustDir(filepath.Join(*root, "custodial/web"), "custodial/web", "../custodial/web"),
		ProvenanceWeb: mustDir(filepath.Join(*root, "provenance/web"), "provenance/web", "../provenance/web"),
		SharedWeb:     webapp.SharedDir(filepath.Join(*root, "common/web"), "common/web", "../common/web"),
		SecureCookie:  *secure,
	}
	if cfg.SharedWeb == "" {
		log.Fatal("cannot find common/web; run from the repository root")
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           app.New(cfg).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("listening on http://%s", *addr)
	log.Fatal(srv.ListenAndServe())
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustDir(candidates ...string) string {
	dir, err := webapp.WebDir(candidates...)
	if err != nil {
		log.Fatal(err)
	}
	return dir
}

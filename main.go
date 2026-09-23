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
	"runtime/debug"
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
	log.Printf("build %s, API version %d", build(), app.APIVersion)

	cfg := app.Config{
		Users:         settings.Users,
		Master:        settings.Master,
		Secret:        settings.Secret,
		Store:         &store.Store{Dir: *dataDir},
		CustodialWeb:  mustDir(filepath.Join(*root, "custodial/web"), "custodial/web", "../custodial/web"),
		ProvenanceWeb: mustDir(filepath.Join(*root, "provenance/web"), "provenance/web", "../provenance/web"),
		SharedWeb:     webapp.SharedDir(filepath.Join(*root, "common/web"), "common/web", "../common/web"),
		SecureCookie:  *secure,
		Build:         build(),
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

// stamp is set at link time (-ldflags "-X main.stamp=...") where the build
// has no git metadata to embed.
var stamp string

// build names the source the binary was compiled from: the commit, marked
// "+dirty" when the working tree had changes. The frontends are read from
// disk at every request, so they can be newer than the binary; this is what
// tells the two apart.
//
// A Docker build has no .git to read, so it passes the commit in instead:
//
//	docker build --build-arg BUILD=$(git rev-parse --short HEAD) .
func build() string {
	if stamp != "" {
		return stamp
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty, at := "", false, ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		case "vcs.time":
			at = s.Value
		}
	}
	if rev == "" {
		return "unknown (built outside a git checkout)"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "+dirty"
	}
	if at != "" {
		rev += " " + at
	}
	return rev
}

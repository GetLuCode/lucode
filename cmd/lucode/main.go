package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"

	apipkg "claude-orchestrator/internal/api"
	dbpkg "claude-orchestrator/internal/db"
	sessionpkg "claude-orchestrator/internal/session"
	wspkg "claude-orchestrator/internal/ws"
	webui "claude-orchestrator/web"
)

func main() {
	port := flag.Int("port", 8090, "HTTP listen port")
	dataDir := flag.String("data", defaultDataDir(), "Directory for SQLite database")
	workingDir := flag.String("working-dir", "", "Base directory for workspace picker")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}

	dbPath := filepath.Join(*dataDir, "lucode.db")
	db, err := dbpkg.Init(dbPath)
	if err != nil {
		log.Fatalf("db init: %v", err)
	}
	defer db.Close()

	hub := wspkg.NewHub()

	backendWSURL := fmt.Sprintf("ws://localhost:%d", *port)
	manager := sessionpkg.NewManager(db, hub, backendWSURL)

	handler := apipkg.New(db, hub, manager, *workingDir)

	// Strip the "dist/" prefix so the SPA FS is rooted at dist/index.html.
	distFS, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		log.Fatalf("embed sub: %v", err)
	}

	httpHandler := apipkg.WrapWithSPA(handler, distFS)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("lucode listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, httpHandler); err != nil {
		log.Fatal(err)
	}
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".data"
	}
	return filepath.Join(home, ".lucode")
}

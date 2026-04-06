package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	apipkg "claude-orchestrator/internal/api"
	dbpkg "claude-orchestrator/internal/db"
	sessionpkg "claude-orchestrator/internal/session"
	wspkg "claude-orchestrator/internal/ws"
)

func main() {
	port := flag.Int("port", 8090, "HTTP listen port")
	dataDir := flag.String("data", defaultDataDir(), "Directory for SQLite database")
	workingDir := flag.String("working-dir", "", "Base directory for workspace picker")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}

	dbPath := filepath.Join(*dataDir, "orchestrator.db")
	db, err := dbpkg.Init(dbPath)
	if err != nil {
		log.Fatalf("db init: %v", err)
	}
	defer db.Close()

	hub := wspkg.NewHub()

	backendWSURL := fmt.Sprintf("ws://localhost:%d", *port)
	manager := sessionpkg.NewManager(db, hub, backendWSURL)

	handler := apipkg.New(db, hub, manager, *workingDir)
	httpHandler := apipkg.Wrap(handler)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("orchestrator listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, httpHandler); err != nil {
		log.Fatal(err)
	}
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".data"
	}
	return filepath.Join(home, ".claude-orchestrator")
}

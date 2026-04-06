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
	hookcfg "claude-orchestrator/internal/hookconfig"
	sessionpkg "claude-orchestrator/internal/session"
	wspkg "claude-orchestrator/internal/ws"
	webui "claude-orchestrator/web"
)

func main() {
	port := flag.Int("port", 8090, "HTTP listen port")
	dataDir := flag.String("data", defaultDataDir(), "Directory for SQLite database and runtime files")
	workingDir := flag.String("working-dir", "", "Base directory for workspace picker")
	claudeConfig := flag.String("claude-config", defaultClaudeConfig(), "Claude config directory (for hook auto-install)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}

	// Auto-install lucodehook into Claude's settings.json if not already present.
	hookPath := hookBinaryPath()
	if err := hookcfg.Ensure(*claudeConfig, hookPath); err != nil {
		log.Printf("warn: could not auto-install hook: %v", err)
	} else {
		log.Printf("hook: registered %s in %s/settings.json", hookPath, *claudeConfig)
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

// hookBinaryPath returns the expected path to lucodehook, co-located with
// the lucode binary itself (e.g. /usr/local/bin/lucodehook).
func hookBinaryPath() string {
	exec, err := os.Executable()
	if err != nil {
		// Fallback: assume it's on PATH / same working dir.
		return "lucodehook"
	}
	return filepath.Join(filepath.Dir(exec), "lucodehook")
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lucode"
	}
	return filepath.Join(home, ".lucode")
}

func defaultClaudeConfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

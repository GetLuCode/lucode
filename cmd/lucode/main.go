package main

import (
	_ "embed"
	"flag"
	"fmt"
	"io"
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

// embeddedHook is the lucodehook binary baked into this executable at build time.
// It is written to ~/.lucode/lucodehook during `lucode install`.
//
//go:embed embedded/lucodehook
var embeddedHook []byte

func main() {
	// Handle subcommands before flag parsing.
	if len(os.Args) > 1 && os.Args[1] == "install" {
		runInstall()
		return
	}

	port := flag.Int("port", 8090, "HTTP listen port")
	dataDir := flag.String("data", defaultDataDir(), "Directory for SQLite database")
	workingDir := flag.String("working-dir", "", "Base directory for workspace picker")
	claudeSettings := flag.String("claude-settings", defaultClaudeSettings(), "Path to Claude settings.json passed to each Claude process")
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
	manager := sessionpkg.NewManager(db, hub, backendWSURL, *claudeSettings)

	handler := apipkg.New(db, hub, manager, *workingDir)

	distFS, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		log.Fatalf("embed sub: %v", err)
	}

	httpHandler := apipkg.WrapWithSPA(handler, distFS)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("lucode listening on http://localhost%s", addr)
	if *claudeSettings != "" {
		log.Printf("claude settings: %s", *claudeSettings)
	}
	if err := http.ListenAndServe(addr, httpHandler); err != nil {
		log.Fatal(err)
	}
}

// runInstall extracts the embedded lucodehook binary and copies lucode itself
// into ~/.lucode/, then writes ~/.lucode/settings.json with the hook registered.
// Does NOT touch ~/.claude/settings.json.
func runInstall() {
	installDir := defaultDataDir() // ~/.lucode

	if err := os.MkdirAll(installDir, 0755); err != nil {
		log.Fatalf("install: mkdir %s: %v", installDir, err)
	}

	// Extract embedded lucodehook.
	hookDst := filepath.Join(installDir, "lucodehook")
	if err := os.WriteFile(hookDst, embeddedHook, 0755); err != nil {
		log.Fatalf("install: write lucodehook: %v", err)
	}
	fmt.Printf("  extracted lucodehook → %s\n", hookDst)

	// Copy lucode binary itself.
	self, err := os.Executable()
	if err != nil {
		log.Fatalf("install: cannot determine executable path: %v", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	lucodeDst := filepath.Join(installDir, "lucode")
	if self == lucodeDst {
		fmt.Printf("  lucode already at    %s\n", lucodeDst)
	} else {
		if err := copyFile(self, lucodeDst, 0755); err != nil {
			log.Fatalf("install: copy lucode: %v", err)
		}
		fmt.Printf("  copied lucode      → %s\n", lucodeDst)
	}

	// Write ~/.lucode/settings.json.
	settingsPath := filepath.Join(installDir, "settings.json")
	if err := hookcfg.WriteSettings(settingsPath, hookDst); err != nil {
		log.Fatalf("install: write settings: %v", err)
	}
	fmt.Printf("  wrote settings     → %s\n", settingsPath)

	fmt.Printf("\nInstall complete! To run:\n\n")
	fmt.Printf("  %s [--port PORT] [--working-dir DIR]\n\n", lucodeDst)
	fmt.Printf("Claude sessions will automatically use %s\n", settingsPath)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lucode"
	}
	return filepath.Join(home, ".lucode")
}

func defaultClaudeSettings() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".lucode", "settings.json")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

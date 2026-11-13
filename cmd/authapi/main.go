// authapi serves the same auth routes the hub now serves in process (internal/authserver), on their
// own port. Only the desktop build still uses it.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"claude-orchestrator/internal/authserver"
	dbpkg "claude-orchestrator/internal/db"
)

func main() {
	addr := flag.String("addr", ":8092", "Listen address")
	dataDir := flag.String("data", defaultDataDir(), "Data directory")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	db, err := dbpkg.Init(filepath.Join(*dataDir, "lucode.db"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	log.Printf("authapi listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, cors(authserver.New(db))))
}

func defaultDataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lucode")
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization,X-API-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

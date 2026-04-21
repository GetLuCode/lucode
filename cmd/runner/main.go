// Command runner is a standalone AI execution service.
// It accepts turn requests via REST, executes them using the Claude CLI or
// Ollama, and publishes all events to NATS so lucode can relay them to
// WebSocket clients and persist them to its database.
//
// Usage:
//
//	runner --port 8091 --nats-url nats://127.0.0.1:4222
//
// Routes:
//
//	POST /api/turns   — start a turn (body: executor.TurnRequest JSON)
//	GET  /health      — 200 OK
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	executorpkg "claude-orchestrator/internal/executor"

	"github.com/nats-io/nats.go"
)

type runner struct {
	nc *nats.Conn

	mu       sync.Mutex
	active   map[string]context.CancelFunc // session_id → cancel
}

func (r *runner) handleTurn(w http.ResponseWriter, req *http.Request) {
	var turnReq executorpkg.TurnRequest
	if err := json.NewDecoder(req.Body).Decode(&turnReq); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if turnReq.SessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}

	r.mu.Lock()
	if _, active := r.active[turnReq.SessionID]; active {
		r.mu.Unlock()
		http.Error(w, "session is busy", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	r.active[turnReq.SessionID] = cancel
	r.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			r.mu.Lock()
			delete(r.active, turnReq.SessionID)
			r.mu.Unlock()
		}()

		subject := "sess." + turnReq.SessionID + ".events"
		executorpkg.Run(ctx, turnReq, func(event map[string]any) {
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			if err := r.nc.Publish(subject, data); err != nil {
				log.Printf("[runner] nats publish %s: %v", subject, err)
			}
		})
	}()

	w.WriteHeader(http.StatusAccepted)
}

func main() {
	port := flag.Int("port", 8091, "HTTP listen port")
	natsURL := flag.String("nats-url", "nats://127.0.0.1:4222", "NATS server URL")
	flag.Parse()

	nc, err := nats.Connect(*natsURL,
		nats.Timeout(10*time.Second),
		nats.RetryOnFailedConnect(false),
	)
	if err != nil {
		log.Fatalf("[runner] nats connect %s: %v", *natsURL, err)
	}
	defer nc.Drain()
	log.Printf("[runner] nats connected — %s", *natsURL)

	r := &runner{
		nc:     nc,
		active: make(map[string]context.CancelFunc),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/turns", r.handleTurn)

	handler := cors(mux)
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[runner] listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if strings.EqualFold(r.Method, http.MethodOptions) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

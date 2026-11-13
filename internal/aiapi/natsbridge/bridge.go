package natsbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"claude-orchestrator/internal/aiapi/api"
	"claude-orchestrator/internal/aiapi/executor"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

const (
	discoverySubject  = "lucode.discovery"
	heartbeatInterval = 15 * time.Second
	streamDoneType    = "stream_done"
)

type natsBridge struct {
	nc         *nats.Conn
	instanceID string
	server     *api.Server
	permNC     *nats.Conn // same conn used for permission gate requests
}

func Start(nc *nats.Conn, server *api.Server, dataDir string) error {
	// The instance id lives in the hub's data dir (the same instance.json the hub uses), so it survives
	// restarts and cannot collide with another program on the host that keeps its own ~/.lucode.
	id, err := loadOrCreateInstanceID(dataDir)
	if err != nil {
		return fmt.Errorf("instance id: %w", err)
	}

	hostname, _ := os.Hostname()
	b := &natsBridge{nc: nc, instanceID: id, server: server, permNC: nc}

	// Per-instance execute subject. The requester publishes here and uses
	// msg.Reply to receive our streaming events back.
	subj := fmt.Sprintf("lucode.aiapi.%s.execute", id)
	if _, err := nc.Subscribe(subj, b.handleExecute); err != nil {
		return fmt.Errorf("subscribe %s: %w", subj, err)
	}

	// Heartbeat so peers on the bus know we exist and can find us by id.
	b.publishHeartbeat(hostname)
	go func() {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for range t.C {
			b.publishHeartbeat(hostname)
		}
	}()

	log.Printf("[aiapi-nats] id=%s listening on %s", id, subj)
	return nil
}

func (b *natsBridge) publishHeartbeat(hostname string) {
	info := map[string]any{
		"id":         b.instanceID,
		"type":       "aiapi",
		"hostname":   hostname,
		"os":         runtime.GOOS,
		"started_at": time.Now().UTC(),
	}
	data, _ := json.Marshal(info)
	b.nc.Publish(discoverySubject, data)
}

// handleExecute parses an execute request from the bus and runs it. Each
// turn event is published back on msg.Reply; the caller reads its own inbox.
func (b *natsBridge) handleExecute(msg *nats.Msg) {
	if msg.Reply == "" {
		log.Printf("[aiapi-nats] execute request without reply subject — ignoring")
		return
	}
	replySubj := msg.Reply

	var req struct {
		SessionID      string                    `json:"session_id"`
		Message        string                    `json:"message"`
		Model          string                    `json:"model"`
		WorkDirs       []string                  `json:"work_dirs"`
		History        []executor.HistoryMessage `json:"history"`
		ImageBase64    string                    `json:"image_base64"`
		ImageMediaType string                    `json:"image_media_type"`
		Thinking       string                    `json:"thinking"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		b.publishEvent(replySubj, map[string]any{
			"type": "session_status", "status": "error",
			"error": "bad request: " + err.Error(),
		})
		b.publishDone(replySubj)
		return
	}
	if req.SessionID == "" {
		req.SessionID = uuid.NewString()
	}
	if req.Model == "" {
		req.Model = "MiniMax-M2.7"
	}

	turnReq := executor.TurnRequest{
		SessionID:      req.SessionID,
		Message:        req.Message,
		Model:          req.Model,
		WorkDirs:       req.WorkDirs,
		History:        req.History,
		ImageBase64:    req.ImageBase64,
		ImageMediaType: req.ImageMediaType,
		Thinking:       req.Thinking,
	}

	var perm *executor.PermissionClient
	if b.permNC != nil {
		perm = &executor.PermissionClient{NC: b.permNC, SessionID: req.SessionID}
	}

	go func() {
		defer b.publishDone(replySubj)
		defer func() {
			if p := recover(); p != nil {
				log.Printf("[aiapi-nats] panic sess=%s: %v", req.SessionID, p)
				b.publishEvent(replySubj, map[string]any{
					"type": "session_status", "status": "error",
					"error": fmt.Sprintf("panic: %v", p),
				})
			}
		}()
		executor.Run(context.Background(), turnReq, func(ev map[string]any) {
			b.publishEvent(replySubj, ev)
		}, perm)
	}()
}

func (b *natsBridge) publishEvent(replySubj string, ev map[string]any) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if err := b.nc.Publish(replySubj, data); err != nil {
		log.Printf("[aiapi-nats] publish event error: %v", err)
	}
}

func (b *natsBridge) publishDone(replySubj string) {
	data, _ := json.Marshal(map[string]string{"type": streamDoneType})
	b.nc.Publish(replySubj, data)
}

func loadOrCreateInstanceID(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, "instance.json")
	type idFile struct {
		ID string `json:"id"`
	}
	if data, err := os.ReadFile(path); err == nil {
		var f idFile
		if json.Unmarshal(data, &f) == nil && f.ID != "" {
			return f.ID, nil
		}
	}
	id := uuid.NewString()
	data, _ := json.Marshal(idFile{ID: id})
	os.WriteFile(path, data, 0644)
	return id, nil
}

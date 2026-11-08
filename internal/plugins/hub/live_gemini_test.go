package hub_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-orchestrator/internal/api"
	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	natspkg "claude-orchestrator/internal/natsbus"
	"claude-orchestrator/internal/plugins"
	"claude-orchestrator/internal/plugins/hub"
)

// A real conversation with Google AI Mode through the real plugin and the real Edge, to see that a
// picture sent with a message arrives. It goes to Google, not to claude, so it costs no tokens; it
// needs a window and a network, so it only runs with LUCODE_AIMODE_LIVE=1. It uses the app's Edge
// (port 9333, the profile with the login), opens a tab of its own and closes it afterwards.
func TestLiveImage(t *testing.T) {
	if os.Getenv("LUCODE_AIMODE_LIVE") != "1" {
		t.Skip("set LUCODE_AIMODE_LIVE=1 to talk to Google")
	}
	// a red square with a blue one inside: easy to describe
	im := image.NewRGBA(image.Rect(0, 0, 240, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 240; x++ {
			c := color.RGBA{220, 30, 30, 255}
			if x > 80 && x < 160 && y > 80 && y < 160 {
				c = color.RGBA{30, 60, 220, 255}
			}
			im.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}

	db, err := dbpkg.Init(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := dbpkg.InsertProject(db, dbpkg.Project{ID: "p1", Name: "p", WorkDirs: []string{"."}, DefaultModel: "haiku"}); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.InsertSession(db, dbpkg.Session{ID: "live-img", ProjectID: "p1", Name: "n", Model: plugins.MappedAIMode, Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	var host plugins.HubHost
	factory := func(d plugins.HubDeps) (plugins.HubHost, error) {
		h, err := hub.New(d)
		host = h
		return h, err
	}
	h := api.New(db, natspkg.NewBus(nil), nil, "")
	if err := h.StartPlugins(context.Background(), t.TempDir(), nil, factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { flows.SetPluginSource(nil) })
	defer func() {
		host.SessionClosed("p1", "live-img") // its tab
		time.Sleep(2 * time.Second)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ans, err := host.Turn(ctx, plugins.TurnRequest{
		SessionID: "live-img", ProjectID: "p1", Model: plugins.MappedAIMode,
		Message:     "Describe la imagen adjunta: ¿de qué colores es y qué formas tiene? Responde en una frase.",
		ImageBase64: base64.StdEncoding.EncodeToString(buf.Bytes()), ImageMediaType: "image/png",
	}, func(text string) { t.Log(text) })
	if err != nil {
		logs, _ := host.Logs(plugins.GeminiID)
		t.Fatalf("%v | logs: %+v", err, logs)
	}
	t.Logf("answer: %s", ans)
	l := strings.ToLower(ans)
	if !strings.Contains(l, "rojo") && !strings.Contains(l, "red") {
		t.Errorf("the answer does not mention the red of the picture")
	}
}

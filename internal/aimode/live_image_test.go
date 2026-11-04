package aimode

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"
)

// A real conversation with Google AI Mode in its own Edge (another port and profile than the
// app's), to see that a picture sent with a message arrives. It goes to Google, not to claude,
// so it costs no tokens; it needs a window and a network, so it only runs with LUCODE_AIMODE_LIVE=1.
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
	// The profile that AI Mode works in is the app's: a new profile lands on the plain search.
	// So the test joins the Edge the app already opened (it opens a tab of its own and closes
	// it afterwards) and never shuts the browser down.
	c := newClient("live-test", Port, "")
	defer func() {
		if tab, ok := c.tabs["live-img"]; ok {
			_ = c.br.CloseTab(tab)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ans, err := c.AskImage(ctx, "live-img", "Describe la imagen adjunta: ¿de qué colores es y qué formas tiene? Responde en una frase.", &Image{Base64: base64.StdEncoding.EncodeToString(buf.Bytes()), MediaType: "image/png"})
	if err != nil {
		pi, _ := c.br.Info()
		body, _ := c.br.Eval(`document.body.innerText.slice(1100, 2000) + " ## scopes=" + [...new Set([...document.querySelectorAll("[data-scope-id]")].map(e=>e.getAttribute("data-scope-id")))].join(",")`)
		t.Fatalf("%v | page: %.80s %q | %s", err, pi.URL, pi.Title, strings.ReplaceAll(body, "\n", " / "))
	}
	t.Logf("answer: %s", ans)
	l := strings.ToLower(ans)
	if !strings.Contains(l, "rojo") && !strings.Contains(l, "red") {
		t.Errorf("the answer does not mention the red of the picture")
	}
}

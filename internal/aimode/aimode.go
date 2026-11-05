// Package aimode is a local "model": it answers by talking to Google's AI Mode (Gemini)
// in a Microsoft Edge that lucode drives over CDP. Each lucode session gets its own tab
// and so its own conversation: the page is opened, "Modo IA" / "AI Mode" is clicked, a
// first message tells Gemini how to answer, and from then on each message of the chat is
// typed into the page and the answer is read back.
package aimode

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"claude-orchestrator/internal/cdp"
)

// Model is the id of this local model as the chat's model picker sends it.
const Model = "local:aimode"

// Port is the debugging port of this Edge, apart from the one the browser_* tools use.
const Port = 9333

// DefaultPrimer is the first message of every conversation. LUCODE_AIMODE_PRIMER replaces it.
const DefaultPrimer = `A partir de ahora eres el asistente de un chat. Reglas para todas tus respuestas:
1. Estructura tu respuesta con claridad: párrafos cortos y listas o encabezados cuando ayuden.
2. Si vas a enviar referencias web, lístalas al final del mensaje bajo el título "Referencias:", cada una con su título y su URL completa.
3. Responde en el mismo idioma en que te escriba el usuario.
4. No inventes nada: si no lo sabes o no encuentras una fuente fiable, dilo claramente.
5. Responde directamente a lo que se pide: sin preámbulos, sin despedidas y sin preguntas de seguimiento ni ofertas de más ayuda.
6. Si vas a mostrar un diagrama (flujos, arquitectura, secuencias, estados, relaciones), nunca lo dibujes con arte ASCII ni con caracteres de línea: dalo en Mermaid, dentro de un bloque de código cuyo lenguaje sea mermaid, con sintaxis válida y los textos entre comillas si llevan paréntesis o signos especiales.
7. Tus respuestas se leen en voz alta con Fish Audio (síntesis de voz, TTS), que interpreta etiquetas de control escritas entre corchetes dentro del propio texto. Inserta esas etiquetas en línea, justo antes de la frase o palabra a la que afectan, para marcar: (a) tus acciones y sonidos corporales, p. ej. [suspira], [se aclara la garganta], [pausa]; (b) el modo en que hablas, p. ej. [susurrando], [en voz baja], [con énfasis], [lentamente]; (c) emociones, p. ej. [alegre], [triste], [sorprendido], [con ironía]; (d) risas y reacciones, p. ej. [ríe], [risita], [riendo]. Escribe cada etiqueta en el mismo idioma de la respuesta, con una o pocas palabras descriptivas, y úsalas con naturalidad y con moderación (no una por frase). Las etiquetas son solo para la voz: no las pongas dentro de bloques de código, diagramas Mermaid, URLs ni en la lista de "Referencias:".
Si entendiste, responde solo: Listo.`

// How long to wait for things on the page.
const (
	pageWait    = 15 * time.Second // elements to show up
	resumeWait  = 3 * time.Minute  // a long saved conversation to load
	giveUpAfter = 20               // polls (10 s) without new turns before a saved conversation is given up
	turnWait    = 25 * time.Second // the new turn to appear after sending
	answerWait  = 3 * time.Minute  // the answer to finish
	poll        = 500 * time.Millisecond
	turnCeiling = 12 * time.Minute // a whole turn, from queueing to the answer
	stableFor   = 5 // polls with the same answer before it counts as finished (about 2.5 s)
)

// Client drives one Edge shared by all sessions that use the model. One question at a time.
type Client struct {
	mu      sync.Mutex
	turn    chan struct{} // one question at a time; unlike a mutex, waiting for it can be cancelled
	br      *cdp.Browser
	port    int
	profile string            // browser profile folder; "" = ~/.lucode/aimode-profile
	tabs    map[string]string // lucode session id -> tab id
	store   Store             // where conversations are remembered; nil = not at all

	lostSaved bool // the saved conversation could not be reopened and a new one was started
}

type progressKey struct{}

// WithProgress makes Ask tell what it is doing ("Reopening the conversation… 12 of 40 turns
// loaded"), so the UI can show it next to the elapsed time.
func WithProgress(ctx context.Context, f func(string)) context.Context {
	return context.WithValue(ctx, progressKey{}, f)
}

func report(ctx context.Context, msg string) {
	if f, ok := ctx.Value(progressKey{}).(func(string)); ok && f != nil {
		f(msg)
	}
}

// Store remembers which Google conversation belongs to a lucode session, so the session can
// continue it after its tab, the browser or lucode itself went away.
type Store interface {
	// ConversationURL is the saved address of the session's conversation ("" if none) and
	// how many turns the page had then: a long conversation loads slowly, and that number
	// says when it has finished.
	ConversationURL(sessID string) (url string, turns int)
	// SaveConversation records the address and turn count after an answer. title is the
	// first thing the user asked; the store keeps the first one it was given.
	SaveConversation(sessID, url, title string, turns int)
}

// SetStore turns on the history of conversations.
func (c *Client) SetStore(s Store) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store = s
}

// newClient makes a client with its own Edge (port, profile folder and state name).
func newClient(name string, port int, profile string) *Client {
	return &Client{br: cdp.New(name, port), port: port, profile: profile, tabs: map[string]string{}, turn: make(chan struct{}, 1)}
}

// Default is the client every session uses.
var Default = newClient("aimode", Port, "")

func headless() bool { return os.Getenv("LUCODE_AIMODE_HEADLESS") == "1" }

func primer() string {
	if p := strings.TrimSpace(os.Getenv("LUCODE_AIMODE_PRIMER")); p != "" {
		return p
	}
	return DefaultPrimer
}

// Image is a picture that goes with a message.
type Image struct {
	Base64    string
	MediaType string
}

// Ask sends text to the conversation of a session and returns Gemini's answer.
func (c *Client) Ask(ctx context.Context, sessID, text string) (string, error) {
	return c.AskImage(ctx, sessID, text, nil)
}

// AskImage is Ask with a picture attached to the message (nil = none).
func (c *Client) AskImage(ctx context.Context, sessID, text string, img *Image) (string, error) {
	// A turn that never ends must not wedge every other conversation: the whole turn has a
	// ceiling, and one that is still waiting for its place can be stopped (Stop cancels ctx).
	ctx, cancel := context.WithTimeout(ctx, turnCeiling)
	defer cancel()
	select {
	case c.turn <- struct{}{}:
	default:
		report(ctx, "Waiting for another Gemini conversation to finish…")
		select {
		case c.turn <- struct{}{}:
		case <-ctx.Done():
			return "", fmt.Errorf("gave up waiting for another Gemini conversation to finish: %w", ctx.Err())
		}
	}
	defer func() { <-c.turn }()
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		return "", errors.New("empty message")
	}
	report(ctx, "Opening Edge…")
	if err := c.ensureBrowser(); err != nil {
		return "", err
	}
	c.lostSaved = false
	fresh, err := c.openConversation(ctx, sessID)
	if err != nil {
		return "", err
	}
	if fresh { // tell it how to answer; its reply to that is not for the user
		report(ctx, "Giving Gemini its instructions…")
		if _, err := c.send(ctx, primer(), nil); err != nil {
			return "", fmt.Errorf("priming the conversation: %w", err)
		}
	}
	answer, err := c.send(ctx, text, img)
	if err != nil {
		return "", err
	}
	c.remember(sessID, text)
	if c.lostSaved {
		answer = "_The saved Google conversation could not be reopened (Google no longer has it for this browser profile), so this is a new one._\n\n" + answer
	}
	return answer, nil
}

// remember saves where the conversation of a session is, so it can be continued later.
func (c *Client) remember(sessID, firstText string) {
	if c.store == nil {
		return
	}
	p, err := c.br.Info()
	if err != nil || p.URL == "" || strings.HasPrefix(p.URL, "about:") {
		return
	}
	c.store.SaveConversation(sessID, p.URL, title(firstText), c.turns())
}

// title is how a conversation is named in the history: what the user asked, on one line.
func title(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if r := []rune(t); len(r) > 100 {
		t = string(r[:100]) + "…"
	}
	return t
}

// Forget closes the tab of a session (a new chat session starts a new conversation).
func (c *Client) Forget(sessID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tab, ok := c.tabs[sessID]; ok {
		delete(c.tabs, sessID)
		_ = c.br.CloseTab(tab)
	}
}

// Shutdown closes the browser and forgets every conversation.
func (c *Client) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tabs = map[string]string{}
	c.br.Close()
}

func (c *Client) ensureBrowser() error {
	// The profile (cookies, local storage) is what Google ties its conversations to, so it is
	// kept between runs: a saved conversation url only works with the profile that made it.
	profile := c.profile
	if profile == "" {
		profile = os.Getenv("LUCODE_AIMODE_PROFILE")
	}
	if profile == "" {
		home, _ := os.UserHomeDir()
		profile = filepath.Join(home, ".lucode", "aimode-profile")
	}
	_, err := c.br.Launch(cdp.LaunchOptions{Headless: headless(), Port: c.port, ProfileDir: profile})
	return err
}

// openConversation points the browser at the session's tab. For a session without one it
// opens google.com and enters AI Mode, and reports fresh=true.
func (c *Client) openConversation(ctx context.Context, sessID string) (fresh bool, err error) {
	if tab, ok := c.tabs[sessID]; ok {
		if _, err := c.br.SwitchTab(tab); err == nil {
			return false, nil
		}
		delete(c.tabs, sessID) // the user closed it: start over
	}
	id, err := c.blankTab()
	if err != nil {
		return false, err
	}
	// The browser lost the tab (it was closed, or lucode restarted) but the conversation is in
	// the history: its url reopens it, with all its turns, in this browser profile.
	if c.store != nil {
		if u, turns := c.store.ConversationURL(sessID); u != "" {
			if c.resume(ctx, u, turns) {
				c.tabs[sessID] = id
				return false, nil
			}
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			// Google no longer has it (another profile, a dropped conversation): say so, then start over
			c.lostSaved = true
		}
	}
	report(ctx, "Starting a new conversation in AI Mode…")
	if err := c.enterAIMode(ctx); err != nil {
		_ = c.br.CloseTab(id)
		return false, err
	}
	c.tabs[sessID] = id
	return true, nil
}

// blankTab makes a tab ready to be pointed somewhere and makes it the current one.
func (c *Client) blankTab() (string, error) {
	tabs, err := c.br.Tabs()
	if err != nil {
		return "", err
	}
	if len(tabs) == 1 && (tabs[0].URL == "about:blank" || tabs[0].URL == "") && len(c.tabs) == 0 {
		if _, err := c.br.SwitchTab(tabs[0].ID); err != nil { // the blank tab Edge opened with
			return "", err
		}
		return tabs[0].ID, nil
	}
	if _, err := c.br.NewTab("about:blank"); err != nil {
		return "", err
	}
	cur, err := c.br.Tabs()
	if err != nil {
		return "", err
	}
	for _, t := range cur {
		if t.Current {
			return t.ID, nil
		}
	}
	return "", errors.New("could not open a tab")
}

// resume opens a saved conversation in the current tab and waits for it to load. A long
// conversation takes a while and shows its turns little by little, so it is loaded when it has
// as many turns as it had when it was saved. A url that no longer works (another browser
// profile, a conversation Google dropped) ends with fewer turns, or none, and the caller
// starts a new conversation instead.
func (c *Client) resume(ctx context.Context, url string, want int) bool {
	if want < 1 {
		want = 1
	}
	report(ctx, "Reopening the saved conversation…")
	if _, err := c.br.Navigate(url); err != nil {
		return false
	}
	deadline := time.Now().Add(resumeWait)
	last, still := -1, 0
	for time.Now().Before(deadline) && ctx.Err() == nil {
		n := c.turns()
		if n != last {
			last, still = n, 0
			if n > 0 {
				report(ctx, fmt.Sprintf("Reopening the saved conversation… %d of %d turns loaded", n, want))
			}
		} else {
			still++
		}
		if n >= want && still >= 3 && c.findInput() != nil {
			return true
		}
		// it stopped loading with fewer turns than it had (or with none): it will not get any better
		if still >= giveUpAfter {
			return false
		}
		time.Sleep(poll)
	}
	return false
}

var aiModeNames = []string{"Modo IA", "AI Mode", "Modo de IA"}

// enterAIMode opens google.com and clicks the "Modo IA" / "AI Mode" button, with the
// direct AI Mode address as the fallback if the button is not there.
func (c *Client) enterAIMode(ctx context.Context) error {
	if _, err := c.br.Navigate("https://www.google.com/"); err != nil {
		return err
	}
	c.acceptConsent()
	entered := false
	if el := c.waitFind(ctx, 8*time.Second, func() []cdp.Element { return c.findByNames(aiModeNames, "link", "button") }); el != nil {
		if _, err := c.br.Click(cdp.ClickOptions{Ref: el.Ref}); err != nil {
			return fmt.Errorf("clicking %q: %w", el.Name, err)
		}
		// The click does not always take the page there (Google's home has shown the button and
		// done nothing): AI Mode is on when its address is. If it is not, use the address.
		for i := 0; i < 8 && !entered && ctx.Err() == nil; i++ {
			time.Sleep(poll)
			p, err := c.br.Info()
			entered = err == nil && strings.Contains(p.URL, "udm=50")
		}
	}
	if !entered {
		if _, err := c.br.Navigate("https://www.google.com/search?udm=50"); err != nil {
			return err
		}
	}
	if c.waitFind(ctx, pageWait, c.findInput) == nil {
		return c.blocked("AI Mode did not show a message box (it may not be available for this account or region)")
	}
	// A plain Google search has a box too: typing the instructions there would run them as a
	// search and leave a dead tab. AI Mode is on only when its address is.
	if p, err := c.br.Info(); err != nil || !strings.Contains(p.URL, "udm=50") {
		return c.blocked("Google showed its plain search instead of AI Mode (it may be asking to verify you are human, or AI Mode is not available for this account or region)")
	}
	return nil
}

// acceptConsent closes Google's cookie dialog if there is one (it appears in some regions).
func (c *Client) acceptConsent() {
	for _, name := range []string{"Aceptar todo", "Accept all", "Acepto", "I agree"} {
		if els, err := c.br.Find(cdp.FindOptions{Role: "button", Name: name, Exact: true, Limit: 1}); err == nil && len(els) > 0 {
			_, _ = c.br.Click(cdp.ClickOptions{Ref: els[0].Ref})
			time.Sleep(time.Second)
			return
		}
	}
}

func (c *Client) findByNames(names []string, roles ...string) []cdp.Element {
	for _, name := range names {
		for _, role := range roles {
			if els, err := c.br.Find(cdp.FindOptions{Role: role, Name: name, Exact: true, Limit: 1}); err == nil && len(els) > 0 {
				return els
			}
		}
	}
	return nil
}

// findInput finds the message box: "Buscar" before the first question, "Haz una pregunta" after.
func (c *Client) findInput() []cdp.Element {
	for _, role := range []string{"textbox", "combobox", "searchbox"} {
		if els, err := c.br.Find(cdp.FindOptions{Role: role, Limit: 1}); err == nil && len(els) > 0 {
			return els
		}
	}
	return nil
}

func (c *Client) waitFind(ctx context.Context, d time.Duration, find func() []cdp.Element) *cdp.Element {
	deadline := time.Now().Add(d)
	for {
		if els := find(); len(els) > 0 {
			return &els[0]
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil
		}
		time.Sleep(poll)
	}
}

// blocked explains a page that is not what we expect: a human check, usually.
func (c *Client) blocked(fallback string) error {
	if p, err := c.br.Info(); err == nil && (strings.Contains(p.URL, "/sorry/") || strings.Contains(strings.ToLower(p.Title), "unusual traffic")) {
		if headless() {
			return errors.New("Google is asking to verify that you are human, and Edge is hidden (LUCODE_AIMODE_HEADLESS=1). Remove that variable so the window shows, solve the check there, and send the message again")
		}
		return errors.New("Google is asking to verify that you are human. Solve the check in the Edge window lucode opened, then send the message again (the browser profile keeps the result)")
	}
	return errors.New(fallback)
}

const turnCountJS = `document.querySelectorAll('[data-scope-id="turn"]').length`

// answerJS turns the last turn's answer into Markdown (see markdown.js): headings, bold,
// italics, lists, code blocks, tables and links keep the form Gemini gave them.
//
//go:embed markdown.js
var answerJS string

// send types text into the conversation and waits for the answer to finish.
func (c *Client) send(ctx context.Context, text string, img *Image) (string, error) {
	before := c.turns()
	box := c.waitFind(ctx, pageWait, c.findInput)
	if box == nil {
		return "", c.blocked("the message box is not on the page")
	}
	if img != nil {
		if err := c.attach(ctx, box, img); err != nil {
			return "", err
		}
		// the page draws the box again with the picture in it: the old reference is stale
		if box = c.waitFind(ctx, pageWait, c.findInput); box == nil {
			return "", c.blocked("the message box is not on the page")
		}
	}
	if _, err := c.br.Type(cdp.TypeOptions{Ref: box.Ref, Text: text, Submit: true}); err != nil {
		return "", fmt.Errorf("typing the message: %w", err)
	}
	report(ctx, "Waiting for Google's answer…")
	// a new turn appears when Google takes the message
	deadline := time.Now().Add(turnWait)
	for c.turns() <= before {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if time.Now().After(deadline) {
			return "", c.blocked("Google did not take the message")
		}
		time.Sleep(poll)
	}
	// the answer is finished when it is there and stops changing
	deadline = time.Now().Add(answerWait)
	last, same := "", 0
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		raw, _ := c.br.Eval(answerJS)
		if strings.TrimSpace(raw) != "" && raw == last {
			same++
			if same >= stableFor {
				return Clean(raw), nil
			}
		} else {
			last, same = raw, 0
		}
		if time.Now().After(deadline) {
			if strings.TrimSpace(last) != "" {
				return Clean(last), nil // better a late answer than none
			}
			return "", errors.New("AI Mode did not answer in time")
		}
		time.Sleep(poll)
	}
}

//go:embed attach.js
var attachJS string

// attach puts a picture into the message box the way a paste of an image does, and waits for
// the page to take it (it uploads it before the message can be sent).
func (c *Client) attach(ctx context.Context, box *cdp.Element, img *Image) error {
	report(ctx, "Attaching the image…")
	if _, err := c.br.Click(cdp.ClickOptions{Ref: box.Ref}); err != nil {
		return fmt.Errorf("focusing the message box: %w", err)
	}
	b64, _ := json.Marshal(img.Base64)
	mime, _ := json.Marshal(img.MediaType)
	out, err := c.br.Eval("(" + attachJS + ")(" + string(b64) + "," + string(mime) + ")")
	if err != nil {
		return fmt.Errorf("attaching the image: %w", err)
	}
	if out != "ok" {
		return fmt.Errorf("attaching the image: %s", out)
	}
	// the page shows the picture as a chip named after the file once it has taken it; the
	// message cannot be sent before that
	name := "image." + strings.NewReplacer("jpeg", "jpg").Replace(strings.TrimPrefix(img.MediaType, "image/"))
	chip := c.waitFind(ctx, 20*time.Second, func() []cdp.Element {
		els, _ := c.br.Find(cdp.FindOptions{Role: "button", Name: name, Exact: true, Limit: 1})
		return els
	})
	if chip == nil {
		return errors.New("Google did not take the image")
	}
	time.Sleep(time.Second) // it uploads it in the background
	return nil
}

func (c *Client) turns() int {
	out, err := c.br.Eval(turnCountJS)
	if err != nil {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(out, "%d", &n)
	return n
}

var (
	chipCount  = regexp.MustCompile(`^\+\d+$`)
	disclaimer = regexp.MustCompile(`(?i)(puede cometer errores|can make mistakes|may include mistakes|verifica las respuestas|double-check)`)
)

// Clean tidies the Markdown of an answer: it drops the "+3" counters of source pills and
// the "AI can make mistakes" notice if they got through, and collapses blank lines.
func Clean(raw string) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		t := strings.TrimSpace(l)
		if chipCount.MatchString(t) {
			continue
		}
		lines = append(lines, strings.TrimRight(l, " \t"))
	}
	for len(lines) > 0 && (strings.TrimSpace(lines[len(lines)-1]) == "" || disclaimer.MatchString(lines[len(lines)-1])) {
		lines = lines[:len(lines)-1]
	}
	out := strings.Join(lines, "\n")
	out = regexp.MustCompile(`\n{3,}`).ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// IsModel reports whether a model id from the chat picker is this local model.
func IsModel(model string) bool { return model == Model }

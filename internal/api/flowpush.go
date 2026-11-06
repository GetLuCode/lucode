package api

import (
	"context"
	"crypto/ecdh"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// The Push notification node: Web Push from the hub. It does not touch a runner: it is an HTTP
// request to the push service of each browser, like AI evaluate's request to the API.
//
// A browser subscribes with the hub's VAPID public key and gives back an endpoint and two keys.
// To notify it, the hub encrypts the text with those keys and posts it to the endpoint, so the
// push service only carries ciphertext. The VAPID keys are created on first use and kept in
// ~/.lucode/flows-push.json (mode 0600). Losing them disconnects every device until it turns
// notifications on again, so an unreadable file is an error and is never replaced.

const (
	pushCallTimeout = 30 * time.Second
	pushTTLSeconds  = 24 * 60 * 60 // a device that is off gets it if it comes back within a day
	// pushMaxPayload stays under the 4 KB that push services accept, with room for encryption.
	pushMaxPayload  = 3000
	pushMaxTitle    = 200
	pushMaxEndpoint = 2048
	// pushDefaultContact identifies the sender to the push services, which use it to reach
	// whoever runs a misbehaving server. LUCODE_PUSH_CONTACT replaces it.
	pushDefaultContact = "lucode@example.com"
)

var (
	flowPushMu   sync.Mutex
	flowPushPath = func() string {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".lucode", "flows-push.json")
	}
	// flowPushClient does not follow redirects: an endpoint comes from a browser, and the hub
	// should only ever post to the host it was given.
	flowPushClient = &http.Client{
		Timeout:       pushCallTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
)

type vapidKeys struct {
	Public  string `json:"public_key"`
	Private string `json:"private_key"`
}

func flowPushKeys() (vapidKeys, error) {
	flowPushMu.Lock()
	defer flowPushMu.Unlock()
	var k vapidKeys
	data, err := os.ReadFile(flowPushPath())
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &k); err != nil || k.Public == "" || k.Private == "" {
			return vapidKeys{}, fmt.Errorf("%s is damaged: restore it, or delete it and turn notifications on again in every device", flowPushPath())
		}
		return k, nil
	case !os.IsNotExist(err):
		return vapidKeys{}, err
	}
	if k.Private, k.Public, err = webpush.GenerateVAPIDKeys(); err != nil {
		return vapidKeys{}, err
	}
	if data, err = json.Marshal(k); err != nil {
		return vapidKeys{}, err
	}
	if err := os.MkdirAll(filepath.Dir(flowPushPath()), 0o700); err != nil {
		return vapidKeys{}, err
	}
	if err := os.WriteFile(flowPushPath(), data, 0o600); err != nil {
		return vapidKeys{}, err
	}
	return k, nil
}

func pushContact() string {
	if v := strings.TrimSpace(os.Getenv("LUCODE_PUSH_CONTACT")); v != "" {
		return strings.TrimPrefix(v, "mailto:")
	}
	return pushDefaultContact
}

// checkPushSubscription rejects what could not work, or should not be posted to. It runs when a
// browser subscribes, so a bad subscription never reaches the sending code.
func checkPushSubscription(s dbpkg.PushSubscription) error {
	if len(s.Endpoint) > pushMaxEndpoint {
		return errors.New("the endpoint is too long")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("the endpoint must be an https URL")
	}
	key, err := base64.RawURLEncoding.DecodeString(s.P256dh)
	if err != nil {
		return errors.New("the p256dh key is not base64url")
	}
	if _, err := ecdh.P256().NewPublicKey(key); err != nil {
		return errors.New("the p256dh key is not a P-256 public key")
	}
	auth, err := base64.RawURLEncoding.DecodeString(s.Auth)
	if err != nil || len(auth) != 16 {
		return errors.New("the auth key must be 16 bytes of base64url")
	}
	return nil
}

// cutText shortens a text at a character boundary.
func cutText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max] + "…"
}

// findPushIcon finds the app's icon in the built web, where Parcel gives it a hashed name that
// the service worker has no way to work out.
func findPushIcon(spaFS fs.FS) string {
	if found, err := fs.Glob(spaFS, "icon-192*.png"); err == nil && len(found) > 0 {
		return "/" + found[0]
	}
	return ""
}

// pushPayload is what the service worker receives. It halves the body until it fits, because
// what counts is the encoded size: quotes and line breaks take more room once in JSON.
func pushPayload(m flows.PushMessage, icon string) ([]byte, error) {
	link := "/"
	if m.ProjectID != "" {
		link = "/projects/" + url.PathEscape(m.ProjectID) + "/flows"
		if m.FlowID != "" {
			link += "?" + url.Values{"flow": {m.FlowID}, "run": {m.RunID}}.Encode()
		}
	}
	tag := "lucode"
	if m.Seq != 0 {
		tag = "flow-step-" + strconv.FormatInt(m.Seq, 10)
	}
	title, body := cutText(m.Title, pushMaxTitle), cutText(m.Body, pushMaxPayload)
	for {
		data, err := json.Marshal(map[string]string{"title": title, "body": body, "url": link, "tag": tag, "icon": icon})
		if err != nil {
			return nil, err
		}
		if len(data) <= pushMaxPayload || body == "" {
			return data, nil
		}
		body = cutText(strings.TrimSuffix(body, "…"), len(body)/2)
	}
}

// deliverPush sends one payload to each subscription, at the same time: a dead device must not
// delay the others. A device its push service no longer knows (404, 410) is forgotten.
func deliverPush(ctx context.Context, db *sql.DB, payload []byte, subs []dbpkg.PushSubscription) (flows.PushResult, error) {
	keys, err := flowPushKeys()
	if err != nil {
		return flows.PushResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, pushCallTimeout)
	defer cancel()

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		res      flows.PushResult
		firstErr error
	)
	for _, sub := range subs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gone, err := postPush(ctx, payload, sub, keys)
			if gone {
				err = dbpkg.DeletePushSubscription(db, sub.Endpoint)
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				res.Failed++
				if firstErr == nil {
					firstErr = err
				}
			case gone:
				res.Removed++
			default:
				res.Sent++
			}
		}()
	}
	wg.Wait()
	return res, firstErr
}

// postPush sends to one device and reports whether that device no longer exists.
func postPush(ctx context.Context, payload []byte, sub dbpkg.PushSubscription, keys vapidKeys) (gone bool, err error) {
	// A copy per device: the library appends its padding to the slice it gets, in place, and
	// sends run at the same time.
	resp, err := webpush.SendNotificationWithContext(ctx, slices.Clone(payload), &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{Auth: sub.Auth, P256dh: sub.P256dh},
	}, &webpush.Options{
		HTTPClient:      flowPushClient,
		Subscriber:      pushContact(),
		TTL:             pushTTLSeconds,
		Urgency:         webpush.UrgencyHigh,
		VAPIDPublicKey:  keys.Public,
		VAPIDPrivateKey: keys.Private,
	})
	if err != nil {
		// the error of a failed request quotes the URL, and the endpoint is a secret
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return false, fmt.Errorf("could not reach the push service: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return true, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, fmt.Errorf("the push service rejected this hub's keys (%d): turn notifications off and on again in that device", resp.StatusCode)
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return false, fmt.Errorf("the push service answered %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
}

// SendPush notifies every subscribed device. Reaching one is success, and the output of the step
// counts the rest. Reaching none is an error, so the flow can react through "failed".
func (f flowHost) SendPush(ctx context.Context, msg flows.PushMessage) (flows.PushResult, error) {
	subs, err := dbpkg.ListPushSubscriptions(f.h.db)
	if err != nil {
		return flows.PushResult{}, err
	}
	if len(subs) == 0 {
		return flows.PushResult{}, errors.New("no device has notifications on: turn them on in the Flows tab of that device")
	}
	payload, err := pushPayload(msg, f.h.pushIcon)
	if err != nil {
		return flows.PushResult{}, err
	}
	res, firstErr := deliverPush(ctx, f.h.db, payload, subs)
	switch {
	case res.Sent > 0:
		return res, nil
	case firstErr != nil:
		return res, fmt.Errorf("no device could be reached: %w", firstErr)
	}
	return res, errors.New("no device has notifications on: the ones this hub knew no longer exist, turn them on again in the Flows tab of that device")
}

// ---- API ----

func (h *Handler) registerFlowPush(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/flow-push", h.getFlowPush)
	mux.HandleFunc("PUT /api/flow-push/subscription", h.putPushSubscription)
	mux.HandleFunc("DELETE /api/flow-push/subscription", h.deletePushSubscription)
	mux.HandleFunc("POST /api/flow-push/test", h.testPushSubscription)
}

// getFlowPush gives the browser what it needs to subscribe. The private key never leaves the hub.
func (h *Handler) getFlowPush(w http.ResponseWriter, _ *http.Request) {
	keys, err := flowPushKeys()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"public_key": keys.Public})
}

func (h *Handler) putPushSubscription(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	sub := dbpkg.PushSubscription{Endpoint: body.Endpoint, P256dh: body.Keys.P256dh, Auth: body.Keys.Auth}
	if err := checkPushSubscription(sub); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := dbpkg.SavePushSubscription(h.db, sub); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pushEndpointOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint == "" {
		http.Error(w, "endpoint is required", http.StatusBadRequest)
		return "", false
	}
	return body.Endpoint, true
}

func (h *Handler) deletePushSubscription(w http.ResponseWriter, r *http.Request) {
	endpoint, ok := pushEndpointOf(w, r)
	if !ok {
		return
	}
	if err := dbpkg.DeletePushSubscription(h.db, endpoint); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testPushSubscription notifies one known device, so a person can check it without a flow. Only
// a stored subscription can be the target: this is not a way to post to any URL.
func (h *Handler) testPushSubscription(w http.ResponseWriter, r *http.Request) {
	endpoint, ok := pushEndpointOf(w, r)
	if !ok {
		return
	}
	sub, err := dbpkg.GetPushSubscription(h.db, endpoint)
	if err != nil {
		http.Error(w, "this device has notifications off", http.StatusNotFound)
		return
	}
	payload, err := pushPayload(flows.PushMessage{Title: "lucode", Body: "Notifications work on this device."}, h.pushIcon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	res, err := deliverPush(r.Context(), h.db, payload, []dbpkg.PushSubscription{sub})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if res.Removed > 0 {
		http.Error(w, "the push service no longer knows this device: turn notifications on again", http.StatusGone)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

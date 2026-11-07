package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Hub and runner talk over NATS, which has no authentication on this network, so a message's
// origin only counts if it is signed. The hub keeps an ed25519 key in its data dir and signs every
// delivery and every invocation. A runner knows that key from its pairing (see pairing.go).

// Message kinds.
const (
	KindDeliver = "deliver"
	KindInvoke  = "invoke"
	KindCancel  = "cancel"
	KindUnload  = "unload"
	KindStatus  = "status"
)

// EnvelopeWindow is how far a message's time can be from now.
const EnvelopeWindow = 2 * time.Minute

// Envelope is a signed message from a hub to one runner.
type Envelope struct {
	Kind    string          `json:"kind"`
	Runner  string          `json:"runner"` // who it is for: it cannot be replayed at another runner
	TS      int64           `json:"ts"`     // unix ms
	Nonce   string          `json:"nonce"`
	Payload json.RawMessage `json:"payload"`
	Key     string          `json:"key"` // the hub's public key, base64: it must be the paired one
	Sig     string          `json:"sig"`
}

func (e Envelope) signedBytes() []byte {
	sum := sha256.Sum256(e.Payload)
	return []byte(strings.Join([]string{"lucode-plugin-v1", e.Kind, e.Runner, fmt.Sprint(e.TS), e.Nonce, hex.EncodeToString(sum[:])}, "\n"))
}

// Signer is the hub's key.
type Signer struct {
	priv ed25519.PrivateKey
	pub  string
}

// LoadOrCreateSigner reads the hub's key from its data dir, or makes one.
func LoadOrCreateSigner(dataDir string) (*Signer, error) {
	file := filepath.Join(dataDir, "plugin-hub-key")
	if raw, err := os.ReadFile(file); err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("the plugin key %s is damaged", file)
		}
		return signerFrom(ed25519.NewKeyFromSeed(seed)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(file, []byte(hex.EncodeToString(priv.Seed())), 0o600); err != nil {
		return nil, err
	}
	return signerFrom(priv), nil
}

// NewSigner makes a key that lives in memory (tests).
func NewSigner() *Signer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	return signerFrom(priv)
}

func signerFrom(priv ed25519.PrivateKey) *Signer {
	return &Signer{priv: priv, pub: base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))}
}

// PublicKey is the key a pairing carries to the runner.
func (s *Signer) PublicKey() string { return s.pub }

// Sign wraps a payload for one runner, with a fresh time and nonce.
func (s *Signer) Sign(kind, runner string, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	var n [16]byte
	if _, err := rand.Read(n[:]); err != nil {
		return Envelope{}, err
	}
	e := Envelope{
		Kind: kind, Runner: runner, TS: time.Now().UnixMilli(), Nonce: hex.EncodeToString(n[:]),
		Payload: raw, Key: s.pub,
	}
	e.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, e.signedBytes()))
	return e, nil
}

// Refusal is a signed message's rejection. Code tells the hub what to say.
type Refusal struct {
	Code string
	Msg  string
}

func (r *Refusal) Error() string { return r.Msg }

// Refusal codes.
const (
	RefuseBadSignature = "bad_signature"
	RefuseNotPaired    = "not_paired"
	RefuseReplay       = "replay"
	RefusePolicy       = "policy"
	RefuseNotDelivered = "not_delivered"
)

// Verifier is the runner's side: its pairing and the nonces already seen.
type Verifier struct {
	Runner  string
	DataDir string
	now     func() time.Time
	start   time.Time

	mu   sync.Mutex
	seen map[string]time.Time
}

// NewVerifier makes the runner's verifier. A message older than this start is refused, since
// the nonces seen before a restart are gone.
func NewVerifier(runner, dataDir string) *Verifier {
	v := &Verifier{Runner: runner, DataDir: dataDir, now: time.Now, seen: map[string]time.Time{}}
	v.start = v.now().Add(-time.Second)
	return v
}

// Pairing reads the pairing from disk every time, so "lucode-runner pair" takes effect on a
// running runner, and pairing again revokes the old secret at once.
func (v *Verifier) Pairing() (Pairing, error) {
	p, err := LoadPairing(v.DataDir)
	if err != nil {
		return p, err
	}
	if p.Runner != v.Runner {
		return p, ErrNotPaired
	}
	return p, nil
}

// Open reads a message from the hub this runner is paired with: sealed with the shared secret,
// signed by the hub's key, for this runner, fresh and not seen before. Nothing else gets through.
func (v *Verifier) Open(raw []byte) (Envelope, Pairing, error) {
	var e Envelope
	p, err := v.Pairing()
	if err != nil {
		return e, p, &Refusal{RefuseNotPaired, "this runner is not paired with a hub: an admin creates a code in Settings, Plugins, Runners, and the operator runs lucode-runner pair <code>"}
	}
	var sealed Sealed
	if err := json.Unmarshal(raw, &sealed); err != nil || sealed.Box == "" {
		return e, p, &Refusal{RefuseBadSignature, "this message is not sealed"}
	}
	if sealed.Runner != v.Runner {
		return e, p, &Refusal{RefuseBadSignature, "this message is for another runner"}
	}
	inner, err := Unseal(p.Secret, sealHubToRunner, v.Runner, "", sealed.Box)
	if err != nil {
		return e, p, &Refusal{RefuseNotPaired, "this message was not sealed with this runner's secret: pair it again if the hub made a new code"}
	}
	if err := json.Unmarshal(inner, &e); err != nil {
		return e, p, &Refusal{RefuseBadSignature, "not a signed message"}
	}
	if e.Sig == "" || e.Key == "" || e.Nonce == "" {
		return e, p, &Refusal{RefuseBadSignature, "this message is not signed"}
	}
	if e.Runner != v.Runner {
		return e, p, &Refusal{RefuseBadSignature, "this message is for another runner"}
	}
	if e.Key != p.HubKey {
		return e, p, &Refusal{RefuseBadSignature, "this message is signed by another hub's key"}
	}
	pub, err := base64.StdEncoding.DecodeString(e.Key)
	sig, err2 := base64.StdEncoding.DecodeString(e.Sig)
	if err != nil || err2 != nil || len(pub) != ed25519.PublicKeySize {
		return e, p, &Refusal{RefuseBadSignature, "this message has a damaged signature"}
	}
	if !ed25519.Verify(pub, e.signedBytes(), sig) {
		return e, p, &Refusal{RefuseBadSignature, "this message's signature does not match"}
	}
	now := v.now()
	at := time.UnixMilli(e.TS)
	if at.Before(now.Add(-EnvelopeWindow)) || at.After(now.Add(EnvelopeWindow)) {
		return e, p, &Refusal{RefuseReplay, "this message is too old or from the future: check the clocks of hub and runner"}
	}
	if at.Before(v.start) {
		return e, p, &Refusal{RefuseReplay, "this message is older than this runner's start"}
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	for n, t := range v.seen {
		if now.Sub(t) > 2*EnvelopeWindow {
			delete(v.seen, n)
		}
	}
	if _, dup := v.seen[e.Nonce]; dup {
		return e, p, &Refusal{RefuseReplay, "this message was already used"}
	}
	v.seen[e.Nonce] = now
	return e, p, nil
}

// SetClock replaces the verifier's clock (tests).
func (v *Verifier) SetClock(now func() time.Time) { v.now = now }

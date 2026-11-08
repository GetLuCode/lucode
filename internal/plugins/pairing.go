package plugins

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Pairing: a runner takes plugins only from a hub an operator paired it with. The hub makes a
// secret for that one runner, and the code it shows carries the secret and the hub's public key, so
// nothing about the pairing crosses the network. Everything in both directions is then sealed with a
// key derived from the secret: the network can neither read it nor forge it, and on top of that
// the hub signs what it sends, so a compromised runner cannot speak as the hub to another runner.

// The directions of sealed traffic. Each has its own key, so a message cannot be sent back.
const (
	sealHubToRunner = "hub->runner"
	sealRunnerToHub = "runner->hub"
	sealReplyToHub  = "reply runner->hub"
	sealReplyToRun  = "reply hub->runner"
)

const pairingCodePrefix = "lucode-pair-1."

// ErrNotPaired means this runner has no pairing, or none with this hub.
var ErrNotPaired = errors.New("this runner is not paired with a hub")

// Pairing is what both sides of a pair know.
type Pairing struct {
	Runner string `json:"runner"`
	HubKey string `json:"hub_key"` // the hub's public signing key
	Secret []byte `json:"secret"`
}

// NewPairingSecret makes the secret of one runner.
func NewPairingSecret() ([]byte, error) {
	s := make([]byte, 32)
	_, err := rand.Read(s)
	return s, err
}

// PairingCode is what an operator carries to the runner.
func PairingCode(p Pairing) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return pairingCodePrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// ParsePairingCode reads a code.
func ParsePairingCode(code string) (Pairing, error) {
	var p Pairing
	body, ok := strings.CutPrefix(strings.TrimSpace(code), pairingCodePrefix)
	if !ok {
		return p, errors.New("this is not a pairing code")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || json.Unmarshal(raw, &p) != nil {
		return p, errors.New("this pairing code is damaged: copy it whole")
	}
	if p.Runner == "" || p.HubKey == "" || len(p.Secret) != 32 {
		return p, errors.New("this pairing code is damaged: copy it whole")
	}
	return p, nil
}

// PairingFile is where a runner keeps its pairing.
func PairingFile(dataDir string) string { return filepath.Join(dataDir, "plugins", "pairing.json") }

// SavePairing writes a runner's pairing, readable by its owner only. The rename makes a running
// runner see the old pairing or the new one, never half of one.
func SavePairing(dataDir string, p Pairing) error {
	file := PairingFile(dataDir)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := restrictToOwner(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("the pairing file could not be made private: %w", err)
	}
	return os.Rename(tmp, file)
}

// LoadPairing reads a runner's pairing.
func LoadPairing(dataDir string) (Pairing, error) {
	var p Pairing
	raw, err := os.ReadFile(PairingFile(dataDir))
	if err != nil {
		return p, ErrNotPaired
	}
	if json.Unmarshal(raw, &p) != nil || len(p.Secret) != 32 || p.HubKey == "" {
		return p, ErrNotPaired
	}
	return p, nil
}

func sealKey(secret []byte, purpose, runner string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, secret, nil, "lucode-plugin-v1 "+purpose+" "+runner, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts and authenticates a message of one purpose for one runner. context is what the
// message must be bound to, such as the request it answers.
func Seal(secret []byte, purpose, runner, context string, plain []byte) (string, error) {
	aead, err := sealKey(secret, purpose, runner)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, []byte(context))), nil
}

// Unseal is the other end of Seal.
func Unseal(secret []byte, purpose, runner, context, box string) ([]byte, error) {
	aead, err := sealKey(secret, purpose, runner)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(box)
	if err != nil || len(raw) < aead.NonceSize() {
		return nil, errors.New("this message cannot be opened")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(context))
	if err != nil {
		return nil, errors.New("this message cannot be opened")
	}
	return plain, nil
}

// Sealed is what travels on the wire.
type Sealed struct {
	Runner string `json:"runner"`
	Box    string `json:"box,omitempty"`
	// Plain is only ever a refusal from a runner that has no key to seal with. It can make a call
	// fail, and nothing else: a value is never read from it.
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// SealRequest wraps a signed message for the runner it names. nonce binds the answer to it.
func SealRequest(secret []byte, s *Signer, kind, runner string, payload any) (raw []byte, nonce string, err error) {
	env, err := s.Sign(kind, runner, payload)
	if err != nil {
		return nil, "", err
	}
	inner, err := json.Marshal(env)
	if err != nil {
		return nil, "", err
	}
	box, err := Seal(secret, sealHubToRunner, runner, "", inner)
	if err != nil {
		return nil, "", err
	}
	raw, err = json.Marshal(Sealed{Runner: runner, Box: box})
	return raw, env.Nonce, err
}

// SealReply seals a runner's answer to the request with this nonce.
func SealReply(secret []byte, runner, nonce string, reply RunnerReply) ([]byte, error) {
	plain, err := json.Marshal(reply)
	if err != nil {
		return nil, err
	}
	box, err := Seal(secret, sealReplyToHub, runner, nonce, plain)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Sealed{Runner: runner, Box: box})
}

// OpenReply reads a runner's answer. Only this runner, answering this request, can have written it.
func OpenReply(secret []byte, runner, nonce string, raw []byte) (RunnerReply, error) {
	var reply RunnerReply
	var s Sealed
	if err := json.Unmarshal(raw, &s); err != nil {
		return reply, errors.New("the runner gave an answer that cannot be read")
	}
	if s.Box == "" { // a runner with no key: this can only say no
		if s.Code != RefuseNotPaired && s.Code != RefusePolicy {
			return reply, errors.New("the runner gave an answer that is not sealed")
		}
		return RunnerReply{Code: s.Code, Error: s.Error}, nil
	}
	plain, err := Unseal(secret, sealReplyToHub, runner, nonce, s.Box)
	if err != nil {
		return reply, errors.New("the answer did not come from this runner, or was changed on the way")
	}
	if err := json.Unmarshal(plain, &reply); err != nil {
		return reply, errors.New("the runner gave an answer that cannot be read")
	}
	return reply, nil
}

// SealCall seals lucode.server.call from a runner. Its time and nonce stop a replay.
func SealCall(secret []byte, runner string, call ServerCall) ([]byte, string, error) {
	plain, err := json.Marshal(call)
	if err != nil {
		return nil, "", err
	}
	box, err := Seal(secret, sealRunnerToHub, runner, "", plain)
	if err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(Sealed{Runner: runner, Box: box})
	return raw, call.Nonce, err
}

// OpenCall reads lucode.server.call on the hub.
func OpenCall(secret []byte, runner, box string) (ServerCall, error) {
	var call ServerCall
	plain, err := Unseal(secret, sealRunnerToHub, runner, "", box)
	if err != nil {
		return call, err
	}
	err = json.Unmarshal(plain, &call)
	return call, err
}

// SealCallReply seals the hub's answer to a call with this nonce.
func SealCallReply(secret []byte, runner, nonce string, reply RunnerReply) ([]byte, error) {
	plain, err := json.Marshal(reply)
	if err != nil {
		return nil, err
	}
	box, err := Seal(secret, sealReplyToRun, runner, nonce, plain)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Sealed{Runner: runner, Box: box})
}

// OpenCallReply reads the hub's answer on the runner.
func OpenCallReply(secret []byte, runner, nonce string, raw []byte) (RunnerReply, error) {
	var reply RunnerReply
	var s Sealed
	if err := json.Unmarshal(raw, &s); err != nil || s.Box == "" {
		if s.Error != "" {
			return reply, errors.New(s.Error)
		}
		return reply, errors.New("the hub gave an answer that cannot be read")
	}
	plain, err := Unseal(secret, sealReplyToRun, runner, nonce, s.Box)
	if err != nil {
		return reply, errors.New("the answer did not come from the hub, or was changed on the way")
	}
	err = json.Unmarshal(plain, &reply)
	return reply, err
}

// SealChunkRequest asks for part of a package.
func SealChunkRequest(secret []byte, runner, sha string, offset int64) ([]byte, error) {
	plain, err := json.Marshal(ChunkRequest{Offset: offset})
	if err != nil {
		return nil, err
	}
	box, err := Seal(secret, sealRunnerToHub, runner, "chunk "+sha, plain)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Sealed{Runner: runner, Box: box})
}

// OpenChunkRequest reads that request on the hub.
func OpenChunkRequest(secret []byte, runner, sha, box string) (ChunkRequest, error) {
	var req ChunkRequest
	plain, err := Unseal(secret, sealRunnerToHub, runner, "chunk "+sha, box)
	if err != nil {
		return req, err
	}
	err = json.Unmarshal(plain, &req)
	return req, err
}

// SealChunk seals part of a package, tied to where it goes in the file.
func SealChunk(secret []byte, runner, sha string, offset int64, data []byte) (string, error) {
	return Seal(secret, sealHubToRunner, runner, fmt.Sprintf("chunk %s %d", sha, offset), data)
}

// OpenChunk reads part of a package on the runner.
func OpenChunk(secret []byte, runner, sha string, offset int64, box string) ([]byte, error) {
	return Unseal(secret, sealHubToRunner, runner, fmt.Sprintf("chunk %s %d", sha, offset), box)
}

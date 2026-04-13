package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

// HashPassword hashes a password with a random salt using SHA256.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	// Use a fresh slice for the hash input to avoid aliasing issues with append.
	payload := make([]byte, 0, 16+len(password))
	payload = append(payload, salt...)
	payload = append(payload, []byte(password)...)
	hashArr := sha256.Sum256(payload)

	combined := make([]byte, 0, 48)
	combined = append(combined, salt...)
	combined = append(combined, hashArr[:]...)
	return base64.StdEncoding.EncodeToString(combined), nil
}

// VerifyPassword checks if a password matches a hash.
func VerifyPassword(password, hash string) (bool, error) {
	combined, err := base64.StdEncoding.DecodeString(hash)
	if err != nil {
		return false, err
	}
	if len(combined) != 48 { // 16 salt + 32 hash
		return false, fmt.Errorf("invalid hash format")
	}
	// Copy salt to a fresh slice to avoid aliasing: combined has cap=48, so
	// append(combined[:16], ...) would overwrite combined[16:] (storedHash).
	salt := make([]byte, 16)
	copy(salt, combined[:16])
	storedHash := combined[16:]

	payload := make([]byte, 0, 16+len(password))
	payload = append(payload, salt...)
	payload = append(payload, []byte(password)...)
	computed := sha256.Sum256(payload)
	return subtle.ConstantTimeCompare(computed[:], storedHash) == 1, nil
}

// GenerateToken generates a cryptographically secure random token.
func GenerateToken(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// GenerateAPIKey generates a key in format "sk_<prefix>_<random>" and returns (fullKey, hash, prefix).
func GenerateAPIKey() (fullKey, keyHash, prefix string, err error) {
	// Generate 12 random bytes for prefix (24 base64 chars) and 32 for secret (44 base64 chars)
	prefixBytes := make([]byte, 12)
	if _, err := rand.Read(prefixBytes); err != nil {
		return "", "", "", err
	}
	prefix = base64.URLEncoding.EncodeToString(prefixBytes)[:16] // trim to 16 chars

	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", "", "", err
	}
	secret := base64.URLEncoding.EncodeToString(secretBytes)

	fullKey = fmt.Sprintf("sk_%s_%s", prefix, secret)

	// Hash the full key for storage
	h := sha256.Sum256([]byte(fullKey))
	keyHash = base64.StdEncoding.EncodeToString(h[:])

	return fullKey, keyHash, prefix, nil
}

// VerifyAPIKey checks if a full key matches a stored hash.
func VerifyAPIKey(fullKey, keyHash string) bool {
	h := sha256.Sum256([]byte(fullKey))
	computed := base64.StdEncoding.EncodeToString(h[:])
	return computed == keyHash
}

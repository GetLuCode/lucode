package tests

import (
	"testing"

	authpkg "claude-orchestrator/internal/auth"
)

func TestPasswordHashing(t *testing.T) {
	password := "test-password-123"

	// Hash the password
	hashStr, err := authpkg.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword error: %v", err)
	}
	t.Logf("Generated hash: %s (len=%d)", hashStr[:min(20, len(hashStr))]+"...", len(hashStr))

	// Verify with correct password
	ok, err := authpkg.VerifyPassword(password, hashStr)
	t.Logf("VerifyPassword(correct): ok=%v, err=%v", ok, err)
	if err != nil {
		t.Fatalf("VerifyPassword error: %v", err)
	}
	if !ok {
		t.Fatalf("VerifyPassword should return true for correct password")
	}

	// Verify with wrong password
	ok, err = authpkg.VerifyPassword("wrong-password", hashStr)
	t.Logf("VerifyPassword(wrong): ok=%v, err=%v", ok, err)
	if err != nil {
		t.Fatalf("VerifyPassword error: %v", err)
	}
	if ok {
		t.Fatalf("VerifyPassword should return false for wrong password")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestTokenGeneration(t *testing.T) {
	token1, err := authpkg.GenerateToken(32)
	if err != nil {
		t.Fatalf("GenerateToken error: %v", err)
	}

	token2, err := authpkg.GenerateToken(32)
	if err != nil {
		t.Fatalf("GenerateToken error: %v", err)
	}

	// Tokens should be different
	if token1 == token2 {
		t.Fatalf("GenerateToken should produce different tokens")
	}

	// Tokens should be non-empty and contain no null bytes
	if len(token1) == 0 || len(token2) == 0 {
		t.Fatalf("Generated tokens should not be empty")
	}
}

func TestAPIKeyGeneration(t *testing.T) {
	fullKey1, hash1, prefix1, err := authpkg.GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey error: %v", err)
	}

	fullKey2, hash2, prefix2, err := authpkg.GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey error: %v", err)
	}

	// Keys should be different
	if fullKey1 == fullKey2 {
		t.Fatalf("GenerateAPIKey should produce different keys")
	}

	// Hashes should be different
	if hash1 == hash2 {
		t.Fatalf("GenerateAPIKey should produce different hashes")
	}

	// Prefixes might collide (unlikely) but should be reasonable length
	if len(prefix1) != 16 || len(prefix2) != 16 {
		t.Fatalf("Prefix should be 16 chars, got %d and %d", len(prefix1), len(prefix2))
	}

	// Full key should match format: sk_<prefix>_<secret>
	if len(fullKey1) < 20 || fullKey1[:3] != "sk_" {
		t.Fatalf("Full key should start with 'sk_', got: %s", fullKey1[:10])
	}

	// VerifyAPIKey should work
	if !authpkg.VerifyAPIKey(fullKey1, hash1) {
		t.Fatalf("VerifyAPIKey should return true for correct key")
	}

	// VerifyAPIKey should fail for wrong hash
	if authpkg.VerifyAPIKey(fullKey1, hash2) {
		t.Fatalf("VerifyAPIKey should return false for wrong hash")
	}
}

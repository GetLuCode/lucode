# Multiuser & Credentials Feeder Implementation

## Overview

This implementation adds multiuser support to lucode with API key-based credential management. Users can now:
- Sign up and log in with username/password
- Create API keys for machine-to-machine access
- Store OAuth tokens (access_token, refresh_token, expires_at) directly in the database instead of relying on `.credentials.json` files
- (Historical) The `credfeeder` tool pushed credentials from Claude CLI config directories to lucode; see "Credentials keeper" below

## Database Schema Changes

### New Tables

#### `users`
```sql
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    DATETIME NOT NULL
)
```
Stores web UI user accounts with salted SHA256 password hashes.

#### `sessions_auth`
```sql
CREATE TABLE sessions_auth (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL
)
```
Web UI session tokens with 24-hour expiry.

#### `api_keys`
```sql
CREATE TABLE api_keys (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL DEFAULT '',
    key_hash   TEXT NOT NULL UNIQUE,
    prefix     TEXT NOT NULL,
    created_at DATETIME NOT NULL
)
```
API keys for programmatic access (e.g., credfeeder). Keys are stored as hashes; full key is shown only at creation time.

### Schema Extensions

#### `claude_accounts` — New Columns
- `access_token` TEXT (OAuth access token from Claude)
- `refresh_token` TEXT (OAuth refresh token)
- `expires_at` INTEGER (Unix milliseconds when token expires)

These allow accounts to store OAuth credentials directly without relying on `.credentials.json` file access.

## API Endpoints

### Authentication

```
POST /api/auth/signup
{
  "username": "user@example.com",
  "password": "password123"
}
→ 201: { "user_id": "uuid" }

POST /api/auth/login
{
  "username": "user@example.com",
  "password": "password123"
}
→ 200: { "token": "session_token...", "user_id": "uuid" }

POST /api/auth/logout
Authorization: Bearer <session_token>
→ 204
```

### API Keys

```
POST /api/apikeys
Authorization: Bearer <session_token>
{ "name": "credfeeder-main" }
→ 201: { "id": "...", "name": "...", "prefix": "...", "key": "sk_..._...", ... }

GET /api/apikeys
Authorization: Bearer <session_token>
→ 200: [ { "id": "...", "name": "...", "prefix": "...", "created_at": "..." }, ... ]

DELETE /api/apikeys/{id}
Authorization: Bearer <session_token>
→ 204
```

### Account Credentials Update

```
PUT /api/accounts/{id}/credentials
X-API-Key: <api_key>
{
  "access_token": "claude_token_...",
  "refresh_token": "refresh_...",
  "expires_at": 1234567890000
}
→ 200: { "id": "...", "name": "...", "access_token": "...", ... }
```

This endpoint requires **API key authentication** (not session token), which the removed credfeeder used.

## Credentials keeper (replaces `cmd/credfeeder`)

Claude accounts and `PUT /api/accounts/{id}/credentials` no longer exist (the routes answer 410), so the
standalone credfeeder had nothing to push to and was removed. What it did that is still useful, keeping a
Claude login from expiring while nobody uses it, now runs inside `lucode-runner` (`internal/claudecreds`):

- Every pass it reads `.credentials.json` of the default Claude config dir (`$CLAUDE_CONFIG_DIR` or `~/.claude`)
  and of every preconfigured AI of the runner.
- A token with 5 minutes or less left is traded for a new pair at Claude's OAuth endpoint and written back
  (the refresh token rotates, so the new one is saved; the other fields of the file are kept).
- It sleeps until 5 minutes before the next expiry (at least 1 minute, at most 1 hour).
- Nothing leaves the machine. `--no-claude-refresh` (also on `lucode-runner start`) turns it off.

## Authentication Utilities (`internal/auth`)

### Functions

- `HashPassword(password) → (hash, error)`
  - SHA256 with 16-byte random salt

- `VerifyPassword(password, hash) → (bool, error)`
  - Constant-time comparison

- `GenerateToken(length) → (token, error)`
  - Cryptographically secure random token (base64 URL-encoded)

- `GenerateAPIKey() → (fullKey, hash, prefix, error)`
  - Returns: `sk_<prefix>_<secret>`, hash for storage, 16-char prefix
  - Hash is stored; full key shown only at creation

- `VerifyAPIKey(fullKey, hash) → bool`
  - Compare hashed key to stored hash

## Handler Updates

### New Methods

- `signup(w, r)` — POST /api/auth/signup
- `login(w, r)` — POST /api/auth/login
- `logout(w, r)` — POST /api/auth/logout
- `createAPIKey(w, r)` — POST /api/apikeys
- `listAPIKeys(w, r)` — GET /api/apikeys
- `deleteAPIKey(w, r)` — DELETE /api/apikeys/{id}
- `updateAccountCredentials(w, r)` — PUT /api/accounts/{id}/credentials

### Updated Methods

- `fetchAnthropicRaw(accountID, url, oauthBeta)`
  - **Changed signature** from `fetchAnthropicRaw(claudeConfigDir, ...)`
  - Now accepts account UUID, looks up account in DB
  - Uses stored `access_token` if available, falls back to `.credentials.json`
  - This allows gradual migration: old file-based accounts still work

### Auth Helpers

- `extractBearerToken(r) → token`
- `requireAuth(r) → (userID, error)` — validates session token
- `requireAPIKey(r) → (userID, error)` — validates API key

## Migration Path

The system supports a **gradual migration**:

1. **Old mode**: Claude accounts with `claude_config_dir` → `fetchAnthropicRaw` reads `.credentials.json`
2. **New mode**: Claude accounts with `access_token` + `refresh_token` stored in DB
3. **Hybrid mode**: Both mechanisms work; stored tokens take precedence

This allows existing setups to continue working while new deployments use the database-backed approach.

## Usage Example

### Setup

1. Create a user:
```bash
curl -X POST http://localhost:8090/api/auth/signup \
  -H "Content-Type: application/json" \
  -d '{"username":"user1","password":"pass123"}'
# Returns: { "user_id": "abc..." }
```

2. Log in:
```bash
curl -X POST http://localhost:8090/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"user1","password":"pass123"}'
# Returns: { "token": "session_...", "user_id": "abc..." }
```

3. Create API key:
```bash
curl -X POST http://localhost:8090/api/apikeys \
  -H "Authorization: Bearer session_..." \
  -H "Content-Type: application/json" \
  -d '{"name":"credfeeder"}'
# Returns: { "id": "...", "key": "sk_abc_xyz...", "prefix": "abc", ... }
```

4. Create a Claude account (API or UI):
```bash
curl -X POST http://localhost:8090/api/accounts \
  -H "Content-Type: application/json" \
  -d '{"name":"my-account","claude_config_dir":"~/.claude"}'
# Returns: { "id": "account-uuid", ... }
```

5. The runner keeps the logins in `~/.claude` renewed by itself (see "Credentials keeper").

## Security Notes

1. **Passwords**: Salted SHA256 hashes (16 bytes salt + 32 bytes hash)
2. **Session tokens**: 48 random bytes (base64 URL-encoded, ~64 chars)
3. **API keys**: SHA256 hashed for storage; full key never stored
4. **API key prefix**: Included in plaintext for identification (like GitHub's `ghp_...`)
5. **Token expiry**: Session tokens expire after 24 hours
6. **Credential storage**: Access/refresh tokens stored in DB; no longer rely on file permissions

## Testing Checklist

- [ ] First user can sign up
- [ ] User can log in with correct password
- [ ] Login fails with wrong password
- [ ] Session token works in Authorization header
- [ ] Session token expires after 24 hours
- [ ] API key can be created and listed
- [ ] API key can be deleted
- [ ] PUT /api/accounts/{id}/credentials works with API key
- [ ] credfeeder successfully reads and pushes credentials
- [ ] credfeeder respects `--once` flag
- [ ] credfeeder handles missing .credentials.json gracefully
- [ ] Existing file-based accounts still work (backward compat)

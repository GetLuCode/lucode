package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	authpkg "claude-orchestrator/internal/auth"
	dbpkg "claude-orchestrator/internal/db"
)

type server struct{ db *sql.DB }

func main() {
	addr    := flag.String("addr", ":8092", "Listen address")
	dataDir := flag.String("data", defaultDataDir(), "Data directory")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	db, err := dbpkg.Init(filepath.Join(*dataDir, "lucode.db"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	s := &server{db: db}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /auth/status", s.status)
	mux.HandleFunc("POST /auth/signup", s.signup)
	mux.HandleFunc("POST /auth/login", s.login)
	mux.HandleFunc("POST /auth/logout", s.logout)
	mux.HandleFunc("POST /auth/validate", s.validate)

	mux.HandleFunc("GET /apikeys", s.listAPIKeys)
	mux.HandleFunc("POST /apikeys", s.createAPIKey)
	mux.HandleFunc("DELETE /apikeys/{id}", s.deleteAPIKey)

	log.Printf("authapi listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, cors(mux)))
}

func defaultDataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lucode")
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization,X-API-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) requireToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", fmt.Errorf("missing token")
	}
	token := auth[7:]
	sess, err := dbpkg.GetAuthSession(s.db, token)
	if err != nil || time.Now().After(sess.ExpiresAt) {
		return "", fmt.Errorf("invalid token")
	}
	return sess.UserID, nil
}

func (s *server) status(w http.ResponseWriter, _ *http.Request) {
	count, _ := dbpkg.CountUsers(s.db)
	jsonResp(w, http.StatusOK, map[string]any{"signup_open": count == 0})
}

func (s *server) signup(w http.ResponseWriter, r *http.Request) {
	count, _ := dbpkg.CountUsers(s.db)
	if count > 0 {
		http.Error(w, "registration is closed", http.StatusForbidden)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" || body.Password == "" {
		http.Error(w, "username and password required", http.StatusBadRequest)
		return
	}
	if _, err := dbpkg.GetUserByUsername(s.db, body.Username); err == nil {
		http.Error(w, "username already taken", http.StatusConflict)
		return
	}
	hash, err := authpkg.HashPassword(body.Password)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	u := dbpkg.User{
		ID:           uuid.New().String(),
		Username:     body.Username,
		PasswordHash: hash,
		IsAdmin:      count == 0,
	}
	if err := dbpkg.InsertUser(s.db, u); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusCreated, map[string]any{"user_id": u.ID, "is_admin": u.IsAdmin})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	u, err := dbpkg.GetUserByUsername(s.db, body.Username)
	if err != nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	if ok, _ := authpkg.VerifyPassword(body.Password, u.PasswordHash); !ok {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	token, err := authpkg.GenerateToken(48)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess := dbpkg.AuthSession{Token: token, UserID: u.ID, ExpiresAt: time.Now().Add(24 * time.Hour)}
	if err := dbpkg.InsertAuthSession(s.db, sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"token": token, "user_id": u.ID, "is_admin": u.IsAdmin})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		dbpkg.DeleteAuthSession(s.db, auth[7:])
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) validate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token  string `json:"token"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if body.Token != "" {
		sess, err := dbpkg.GetAuthSession(s.db, body.Token)
		if err != nil || time.Now().After(sess.ExpiresAt) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		u, err := dbpkg.GetUserByID(s.db, sess.UserID)
		if err != nil {
			http.Error(w, "user not found", http.StatusUnauthorized)
			return
		}
		jsonResp(w, http.StatusOK, map[string]any{"UserID": u.ID, "IsAdmin": u.IsAdmin})
		return
	}

	if body.APIKey != "" {
		h := sha256.Sum256([]byte(body.APIKey))
		keyHash := base64.StdEncoding.EncodeToString(h[:])
		key, err := dbpkg.GetAPIKeyByHash(s.db, keyHash)
		if err != nil {
			http.Error(w, "invalid api key", http.StatusUnauthorized)
			return
		}
		u, _ := dbpkg.GetUserByID(s.db, key.UserID)
		jsonResp(w, http.StatusOK, map[string]any{"UserID": u.ID, "IsAdmin": u.IsAdmin})
		return
	}

	http.Error(w, "token or api_key required", http.StatusBadRequest)
}

func (s *server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	userID, err := s.requireToken(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	keys, err := dbpkg.ListAPIKeys(s.db, userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, keys)
}

func (s *server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, err := s.requireToken(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	fullKey, keyHash, prefix, err := authpkg.GenerateAPIKey()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	k := dbpkg.APIKey{
		ID:      uuid.New().String(),
		UserID:  userID,
		Name:    body.Name,
		KeyHash: keyHash,
		Prefix:  prefix,
	}
	if err := dbpkg.InsertAPIKey(s.db, k); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusCreated, map[string]any{"key": fullKey, "id": k.ID, "prefix": prefix})
}

func (s *server) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, err := s.requireToken(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := dbpkg.DeleteAPIKey(s.db, r.PathValue("id"), userID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func jsonResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/actionbroker"
	"github.com/shiden-guardian/shiden-guardian/internal/auth"
	"github.com/shiden-guardian/shiden-guardian/internal/authlimit"
	"github.com/shiden-guardian/shiden-guardian/internal/config"
	"github.com/shiden-guardian/shiden-guardian/internal/store"
)

type user struct {
	ID, Username, PasswordHash, TOTPSecret string
	Recovery                               []string
}

type contextKey string

const userKey contextKey = "user"

var errRecoveryCodeConsumed = errors.New("recovery code already consumed")

type deniedCounters struct {
	total       atomic.Uint64
	rateLimited atomic.Uint64
}

type server struct {
	cfg         config.Config
	store       *store.Store
	limiter     *authlimit.Limiter
	hashGate    chan struct{}
	dummyHash   string
	actions     *actionbroker.Client
	denied      deniedCounters
	cleanupMu   sync.Mutex
	lastClean   time.Time
	lookupProxy func(context.Context) ([]net.IP, error)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		response, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://127.0.0.1:8081/healthz")
		if err != nil || response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		response.Body.Close()
		return
	}
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	if cfg.EncryptionKey == "" || cfg.BootstrapToken == "" || cfg.ActionBrokerKey == "" {
		slog.Error("config", "error", "ENCRYPTION_KEY, BOOTSTRAP_TOKEN, and ACTION_BROKER_KEY are required")
		os.Exit(1)
	}
	key, err := actionbroker.DecodeKey(cfg.ActionBrokerKey)
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	dummyHash, err := auth.HashPassword("dummy-password-that-is-never-valid-2026")
	if err != nil {
		slog.Error("dummy password", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	db, err := store.Open(ctx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		slog.Error("database", "error", err)
		os.Exit(1)
	}
	defer db.DB.Close()
	s := &server{cfg: cfg, store: db, limiter: authlimit.New(authlimit.DefaultConfig()), hashGate: make(chan struct{}, 2), dummyHash: dummyHash, actions: actionbroker.NewClient(cfg.ActionBrokerSock, key)}
	s.lookupProxy = func(ctx context.Context) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", "caddy")
	}
	go s.maintenance()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/auth/bootstrap/status", s.bootstrapStatus)
	mux.HandleFunc("POST /api/v1/auth/bootstrap/start", s.bootstrapStart)
	mux.HandleFunc("POST /api/v1/auth/bootstrap/confirm", s.bootstrapConfirm)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.Handle("POST /api/v1/auth/logout", s.requireAuth(http.HandlerFunc(s.logout)))
	mux.Handle("POST /api/v1/actions/restart", s.requireAuth(http.HandlerFunc(s.restart)))
	mux.Handle("POST /api/v1/actions/reward-gap/acknowledge", s.requireAuth(http.HandlerFunc(s.acknowledgeRewardGap)))
	mux.Handle("PUT /api/v1/settings", s.requireAuth(http.HandlerFunc(s.updateSettings)))
	handler := securityHeaders(requestLogger(s.caddyOnly(mux)))
	httpServer := &http.Server{Addr: cfg.ListenAddress, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	slog.Info("auth broker listening", "address", cfg.ListenAddress)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("auth broker stopped", "error", err)
		os.Exit(1)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return false
	}
	return true
}

func (s *server) clientIP(r *http.Request) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if net.ParseIP(value) == nil {
		return "", false
	}
	return value, true
}

func (s *server) caddyOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "untrusted_proxy"})
			return
		}
		peer := net.ParseIP(host)
		if peer == nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "untrusted_proxy"})
			return
		}
		if r.URL.Path == "/healthz" && peer.IsLoopback() {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if s.lookupProxy != nil {
			if addresses, lookupErr := s.lookupProxy(ctx); lookupErr == nil {
				for _, address := range addresses {
					if peer.Equal(address) {
						next.ServeHTTP(w, r)
						return
					}
				}
			}
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "untrusted_proxy"})
	})
}

func (s *server) tooMany(w http.ResponseWriter, retryAfter time.Duration) {
	s.denied.total.Add(1)
	s.denied.rateLimited.Add(1)
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_attempts"})
}

func (s *server) rateLimit(w http.ResponseWriter, r *http.Request, username string) (time.Duration, bool) {
	ip, ok := s.clientIP(r)
	if !ok {
		writeJSON(w, 400, map[string]string{"error": "invalid_client_address"})
		return 0, false
	}
	decision := s.limiter.Allow(ip, username)
	if !decision.Allowed {
		s.tooMany(w, decision.RetryAfter)
		return 0, false
	}
	return decision.Delay, true
}

func wait(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *server) acquireHash() bool {
	select {
	case s.hashGate <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *server) releaseHash() { <-s.hashGate }

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.DB.PingContext(ctx); err != nil {
		writeJSON(w, 503, map[string]string{"status": "database_unavailable"})
		return
	}
	if err := s.actions.Health(ctx); err != nil {
		writeJSON(w, 503, map[string]string{"status": "controller_unavailable"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *server) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	var count int
	if err := s.store.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM users WHERE active=true`).Scan(&count); err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	writeJSON(w, 200, map[string]any{"needs_bootstrap": count == 0})
}

func (s *server) checkBootstrap(value string) bool {
	return subtle.ConstantTimeCompare([]byte(value), []byte(s.cfg.BootstrapToken)) == 1
}

func (s *server) bootstrapStart(w http.ResponseWriter, r *http.Request) {
	var body struct{ Token, Username, Password string }
	if !readJSON(w, r, &body) {
		return
	}
	delay, allowed := s.rateLimit(w, r, body.Username)
	if !allowed || !wait(r.Context(), delay) {
		return
	}
	if !s.checkBootstrap(body.Token) {
		s.denied.total.Add(1)
		writeJSON(w, 403, map[string]string{"error": "invalid_bootstrap_token"})
		return
	}
	var active int
	if err := s.store.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM users WHERE active=true`).Scan(&active); err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	if active > 0 {
		writeJSON(w, 409, map[string]string{"error": "already_bootstrapped"})
		return
	}
	if !auth.ValidUsername(body.Username) || !auth.ValidPassword(body.Password) {
		writeJSON(w, 400, map[string]string{"error": "username_or_password_policy"})
		return
	}
	if !s.acquireHash() {
		s.tooMany(w, time.Second)
		return
	}
	passwordHash, err := auth.HashPassword(body.Password)
	s.releaseHash()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	encryptedSecret, err := auth.EncryptSecret(s.cfg.EncryptionKey, secret)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "encryption"})
		return
	}
	plain, hashes, err := auth.RecoveryCodes(8)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	id, err := store.NewID("usr")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM users WHERE active=false`); err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO users(id,username,password_hash,totp_secret,recovery_hashes,active) VALUES($1,$2,$3,$4,$5,false)`, id, body.Username, passwordHash, encryptedSecret, store.JSON(hashes))
	}
	if err != nil || tx.Commit() != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	s.store.Audit(r.Context(), "admin.bootstrap.start", body.Username, "success", map[string]any{})
	writeJSON(w, 201, map[string]any{"secret": secret, "otpauth_uri": auth.TOTPURI(secret, body.Username), "recovery_codes": plain})
}

func (s *server) bootstrapConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		TOTP     string `json:"totp_code"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	delay, allowed := s.rateLimit(w, r, body.Username)
	if !allowed || !wait(r.Context(), delay) {
		return
	}
	if !s.checkBootstrap(body.Token) {
		s.denied.total.Add(1)
		writeJSON(w, 403, map[string]string{"error": "invalid_bootstrap_token"})
		return
	}
	var encryptedSecret string
	err := s.store.DB.QueryRowContext(r.Context(), `SELECT totp_secret FROM users WHERE username=$1 AND active=false`, body.Username).Scan(&encryptedSecret)
	secret, decryptErr := auth.DecryptSecret(s.cfg.EncryptionKey, encryptedSecret)
	if err != nil || decryptErr != nil || !auth.ValidateTOTP(secret, body.TOTP, time.Now()) {
		s.denied.total.Add(1)
		writeJSON(w, 400, map[string]string{"error": "invalid_totp"})
		return
	}
	result, err := s.store.DB.ExecContext(r.Context(), `UPDATE users SET active=true WHERE username=$1 AND active=false`, body.Username)
	rows, _ := result.RowsAffected()
	if err != nil || rows != 1 {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	s.limiter.Success(body.Username)
	s.store.Audit(r.Context(), "admin.bootstrap.confirm", body.Username, "success", map[string]any{})
	writeJSON(w, 200, map[string]string{"status": "active"})
}

func (s *server) loadUser(ctx context.Context, username string) (user, error) {
	var x user
	var raw []byte
	err := s.store.DB.QueryRowContext(ctx, `SELECT id,username,password_hash,totp_secret,recovery_hashes FROM users WHERE username=$1 AND active=true`, username).Scan(&x.ID, &x.Username, &x.PasswordHash, &x.TOTPSecret, &raw)
	if err != nil {
		return x, err
	}
	if err = json.Unmarshal(raw, &x.Recovery); err != nil {
		return x, err
	}
	x.TOTPSecret, err = auth.DecryptSecret(s.cfg.EncryptionKey, x.TOTPSecret)
	return x, err
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp_code"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	delay, allowed := s.rateLimit(w, r, body.Username)
	if !allowed || !wait(r.Context(), delay) {
		return
	}
	if !s.acquireHash() {
		s.tooMany(w, time.Second)
		return
	}
	x, err := s.loadUser(r.Context(), body.Username)
	if errors.Is(err, sql.ErrNoRows) {
		_ = auth.VerifyPassword(s.dummyHash, body.Password)
	} else if err == nil {
		if !auth.VerifyPassword(x.PasswordHash, body.Password) {
			err = sql.ErrNoRows
		}
	}
	s.releaseHash()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	ok := err == nil
	recoveryIndex := -1
	if ok && !auth.ValidateTOTP(x.TOTPSecret, body.TOTP, time.Now()) {
		hash := auth.HashToken(strings.ToUpper(strings.TrimSpace(body.TOTP)))
		for index, item := range x.Recovery {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(item)) == 1 {
				recoveryIndex = index
				break
			}
		}
		ok = recoveryIndex >= 0
	}
	if !ok {
		s.limiter.Fail(body.Username)
		s.denied.total.Add(1)
		writeJSON(w, 401, map[string]string{"error": "invalid_credentials"})
		return
	}
	sessionToken, err := auth.RandomToken(32)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	csrf, err := auth.RandomToken(24)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	defer tx.Rollback()
	if recoveryIndex >= 0 {
		var currentRaw []byte
		if err = tx.QueryRowContext(r.Context(), `SELECT recovery_hashes FROM users WHERE id=$1 FOR UPDATE`, x.ID).Scan(&currentRaw); err == nil {
			var current []string
			err = json.Unmarshal(currentRaw, &current)
			matched := -1
			wanted := auth.HashToken(strings.ToUpper(strings.TrimSpace(body.TOTP)))
			for index, item := range current {
				if subtle.ConstantTimeCompare([]byte(wanted), []byte(item)) == 1 {
					matched = index
					break
				}
			}
			if matched < 0 {
				err = errRecoveryCodeConsumed
			} else {
				current = append(current[:matched], current[matched+1:]...)
				_, err = tx.ExecContext(r.Context(), `UPDATE users SET recovery_hashes=$2 WHERE id=$1`, x.ID, store.JSON(current))
			}
		}
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO sessions(token_hash,user_id,csrf_hash,expires_at) VALUES($1,$2,$3,$4)`, auth.HashToken(sessionToken), x.ID, auth.HashToken(csrf), time.Now().Add(12*time.Hour))
	}
	if errors.Is(err, errRecoveryCodeConsumed) {
		s.limiter.Fail(body.Username)
		s.denied.total.Add(1)
		writeJSON(w, 401, map[string]string{"error": "invalid_credentials"})
		return
	}
	if err != nil || tx.Commit() != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "sg_session", Value: sessionToken, Path: "/", MaxAge: 43200, HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	w.Header().Set("X-CSRF-Token", csrf)
	s.limiter.Success(body.Username)
	s.store.Audit(r.Context(), "session.login", x.Username, "success", map[string]any{})
	writeJSON(w, 200, map[string]any{"username": x.Username})
}

func (s *server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("sg_session")
		if err != nil {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		var x user
		var raw []byte
		var csrfHash string
		var expires time.Time
		err = s.store.DB.QueryRowContext(r.Context(), `SELECT u.id,u.username,u.password_hash,u.totp_secret,u.recovery_hashes,s.csrf_hash,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND u.active=true`, auth.HashToken(cookie.Value)).Scan(&x.ID, &x.Username, &x.PasswordHash, &x.TOTPSecret, &raw, &csrfHash, &expires)
		if err == nil {
			err = json.Unmarshal(raw, &x.Recovery)
		}
		if err == nil {
			x.TOTPSecret, err = auth.DecryptSecret(s.cfg.EncryptionKey, x.TOTPSecret)
		}
		if err != nil || expires.Before(time.Now()) {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			provided := r.Header.Get("X-CSRF-Token")
			if provided == "" || subtle.ConstantTimeCompare([]byte(auth.HashToken(provided)), []byte(csrfHash)) != 1 {
				writeJSON(w, 403, map[string]string{"error": "csrf"})
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, x)))
	})
}

func currentUser(r *http.Request) user { return r.Context().Value(userKey).(user) }

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("sg_session"); err == nil {
		_, _ = s.store.DB.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, auth.HashToken(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "sg_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]string{"status": "logged_out"})
}

func (s *server) stepUp(x user, password, totp string) (valid, busy bool) {
	if !s.acquireHash() {
		return false, true
	}
	ok := auth.VerifyPassword(x.PasswordHash, password)
	s.releaseHash()
	return ok && auth.ValidateTOTP(x.TOTPSecret, totp, time.Now()), false
}

func (s *server) restart(w http.ResponseWriter, r *http.Request) {
	x := currentUser(r)
	var body struct {
		Password   string `json:"password"`
		TOTP       string `json:"totp_code"`
		Reason     string `json:"reason"`
		Confirm    string `json:"confirm"`
		IncidentID string `json:"incident_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if body.Confirm != s.cfg.NodeName || len(reason) < 10 || len(reason) > 1000 {
		s.store.Audit(r.Context(), "restart.request", x.Username, "denied", map[string]any{})
		writeJSON(w, 403, map[string]string{"error": "step_up_failed"})
		return
	}
	valid, busy := s.stepUp(x, body.Password, body.TOTP)
	if busy {
		s.tooMany(w, time.Second)
		return
	}
	if !valid {
		s.store.Audit(r.Context(), "restart.request", x.Username, "denied", map[string]any{})
		writeJSON(w, 403, map[string]string{"error": "step_up_failed"})
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 100 {
		writeJSON(w, 400, map[string]string{"error": "idempotency_key_required"})
		return
	}
	id, err := store.NewID("act")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	status, result, err := s.actions.Submit(r.Context(), actionbroker.ManualAction{ActionID: id, IdempotencyKey: key, IncidentID: body.IncidentID, RequestedBy: x.Username, Reason: reason, ActionKind: actionbroker.ActionRestartService, Parameters: json.RawMessage(`{}`)})
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "controller_unavailable"})
		return
	}
	if status/100 != 2 {
		if result.Error == "" {
			result.Error = "controller_rejected"
		}
		writeJSON(w, status, map[string]string{"error": result.Error})
		return
	}
	s.store.Audit(r.Context(), "restart.request", x.Username, "queued", map[string]string{"action_id": result.ID})
	writeJSON(w, 202, map[string]string{"id": result.ID, "status": "pending"})
}

func (s *server) acknowledgeRewardGap(w http.ResponseWriter, r *http.Request) {
	x := currentUser(r)
	var body struct {
		Password       string `json:"password"`
		TOTP           string `json:"totp_code"`
		Reason         string `json:"reason"`
		Confirm        string `json:"confirm"`
		ExpectedCursor int64  `json:"expected_cursor"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	reason := strings.TrimSpace(body.Reason)
	expectedConfirmation := fmt.Sprintf("PRUNED GAP %d", body.ExpectedCursor)
	if body.ExpectedCursor <= 0 || body.Confirm != expectedConfirmation || len(reason) < 10 || len(reason) > 1000 {
		s.store.Audit(r.Context(), "reward.history_gap.request", x.Username, "denied", map[string]any{})
		writeJSON(w, 403, map[string]string{"error": "step_up_failed"})
		return
	}
	valid, busy := s.stepUp(x, body.Password, body.TOTP)
	if busy {
		s.tooMany(w, time.Second)
		return
	}
	if !valid {
		s.store.Audit(r.Context(), "reward.history_gap.request", x.Username, "denied", map[string]any{})
		writeJSON(w, 403, map[string]string{"error": "step_up_failed"})
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 100 {
		writeJSON(w, 400, map[string]string{"error": "idempotency_key_required"})
		return
	}
	id, err := store.NewID("act")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	parameters, err := json.Marshal(actionbroker.RewardGapParameters{ExpectedCursor: body.ExpectedCursor})
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	status, result, err := s.actions.Submit(r.Context(), actionbroker.ManualAction{
		ActionID:       id,
		IdempotencyKey: key,
		RequestedBy:    x.Username,
		Reason:         reason,
		ActionKind:     actionbroker.ActionAcknowledgePrunedRewardGap,
		Parameters:     parameters,
	})
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "controller_unavailable"})
		return
	}
	if status/100 != 2 {
		if result.Error == "" {
			result.Error = "controller_rejected"
		}
		writeJSON(w, status, map[string]string{"error": result.Error})
		return
	}
	s.store.Audit(r.Context(), "reward.history_gap.request", x.Username, "queued", map[string]any{"action_id": result.ID, "expected_cursor": body.ExpectedCursor})
	writeJSON(w, 202, map[string]string{"id": result.ID, "status": "pending"})
}

func (s *server) updateSettings(w http.ResponseWriter, r *http.Request) {
	x := currentUser(r)
	var body struct {
		AutomationEnabled bool   `json:"automation_enabled"`
		Password          string `json:"password"`
		TOTP              string `json:"totp_code"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	valid, busy := s.stepUp(x, body.Password, body.TOTP)
	if busy {
		s.tooMany(w, time.Second)
		return
	}
	if !valid {
		writeJSON(w, 403, map[string]string{"error": "step_up_failed"})
		return
	}
	if body.AutomationEnabled && time.Now().Before(s.cfg.InstalledAt.Add(s.cfg.ObserveOnlyPeriod)) {
		writeJSON(w, 409, map[string]string{"error": "observe_period_not_finished"})
		return
	}
	if err := s.store.SetSetting(r.Context(), "automation_enabled", body.AutomationEnabled); err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	s.store.Audit(r.Context(), "settings.automation", x.Username, "success", map[string]bool{"enabled": body.AutomationEnabled})
	writeJSON(w, 200, map[string]bool{"automation_enabled": body.AutomationEnabled})
}

func (s *server) maintenance() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		total := s.denied.total.Swap(0)
		rateLimited := s.denied.rateLimited.Swap(0)
		if total > 0 {
			s.store.Audit(context.Background(), "login.denied_summary", "anonymous", "denied", map[string]uint64{"total": total, "rate_limited": rateLimited})
		}
		s.cleanupMu.Lock()
		if time.Since(s.lastClean) >= time.Hour {
			_, _ = s.store.DB.ExecContext(context.Background(), `DELETE FROM sessions WHERE expires_at < now()`)
			s.lastClean = time.Now()
		}
		s.cleanupMu.Unlock()
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

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
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/agentclient"
	"github.com/shiden-guardian/shiden-guardian/internal/auth"
	"github.com/shiden-guardian/shiden-guardian/internal/config"
	"github.com/shiden-guardian/shiden-guardian/internal/model"
	"github.com/shiden-guardian/shiden-guardian/internal/store"
)

type user struct {
	ID, Username, PasswordHash, TOTPSecret string
	Recovery                               []string
}
type contextKey string

const userKey contextKey = "user"

type server struct {
	cfg     config.Config
	store   *store.Store
	agent   *agentclient.Client
	limiter *loginLimiter
}
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string][]time.Time
}

func newLimiter() *loginLimiter { return &loginLimiter{entries: map[string][]time.Time{}} }
func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-15 * time.Minute)
	items := l.entries[key][:0]
	for _, t := range l.entries[key] {
		if t.After(cut) {
			items = append(items, t)
		}
	}
	l.entries[key] = items
	return len(items) < 5
}
func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	l.entries[key] = append(l.entries[key], time.Now())
	l.mu.Unlock()
}
func (l *loginLimiter) clear(key string) { l.mu.Lock(); delete(l.entries, key); l.mu.Unlock() }

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		response, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://127.0.0.1:8080/healthz")
		if err != nil || response.StatusCode != 200 {
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
	if len(os.Args) > 1 && os.Args[1] == "-migrate-dry-run" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := store.DryRunMigrations(ctx, cfg.DatabaseURL); err != nil {
			slog.Error("migration dry-run", "error", err)
			os.Exit(1)
		}
		slog.Info("migration dry-run succeeded")
		return
	}
	if cfg.EncryptionKey == "" {
		slog.Error("config", "error", "ENCRYPTION_KEY is required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database", "error", err)
		os.Exit(1)
	}
	defer db.DB.Close()
	s := &server{cfg: cfg, store: db, agent: agentclient.New(cfg.AgentObserveSock, cfg.AgentControlSock), limiter: newLimiter()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/auth/bootstrap/status", s.bootstrapStatus)
	mux.HandleFunc("POST /api/v1/auth/bootstrap/start", s.bootstrapStart)
	mux.HandleFunc("POST /api/v1/auth/bootstrap/confirm", s.bootstrapConfirm)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.Handle("POST /api/v1/auth/logout", s.requireAuth(http.HandlerFunc(s.logout)))
	mux.Handle("GET /api/v1/overview", s.requireAuth(http.HandlerFunc(s.overview)))
	mux.Handle("GET /api/v1/metrics/{panel}", s.requireAuth(http.HandlerFunc(s.metrics)))
	mux.Handle("GET /api/v1/logs", s.requireAuth(http.HandlerFunc(s.logs)))
	mux.Handle("GET /api/v1/incidents", s.requireAuth(http.HandlerFunc(s.incidents)))
	mux.Handle("GET /api/v1/rewards", s.requireAuth(http.HandlerFunc(s.rewards)))
	mux.Handle("POST /api/v1/diagnoses", s.requireAuth(http.HandlerFunc(s.diagnose)))
	mux.Handle("GET /api/v1/diagnoses", s.requireAuth(http.HandlerFunc(s.diagnoses)))
	mux.Handle("POST /api/v1/actions/restart", s.requireAuth(http.HandlerFunc(s.restart)))
	mux.Handle("GET /api/v1/settings", s.requireAuth(http.HandlerFunc(s.settings)))
	mux.Handle("PUT /api/v1/settings", s.requireAuth(http.HandlerFunc(s.updateSettings)))
	mux.Handle("POST /api/v1/settings/test-smtp", s.requireAuth(http.HandlerFunc(s.testSMTP)))
	mux.Handle("POST /api/v1/settings/test-gemini", s.requireAuth(http.HandlerFunc(s.testGemini)))
	mux.Handle("GET /api/v1/audit", s.requireAuth(http.HandlerFunc(s.audit)))
	mux.Handle("GET /api/v1/events", s.requireAuth(http.HandlerFunc(s.events)))
	handler := securityHeaders(requestLogger(mux))
	server := &http.Server{Addr: cfg.ListenAddress, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	slog.Info("api listening", "address", cfg.ListenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("api stopped", "error", err)
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
	return true
}
func (s *server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.DB.PingContext(ctx); err != nil {
		writeJSON(w, 503, map[string]string{"status": "database_unavailable"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *server) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	var count int
	_ = s.store.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM users WHERE active=true`).Scan(&count)
	writeJSON(w, 200, map[string]any{"needs_bootstrap": count == 0})
}
func (s *server) checkBootstrap(value string) bool {
	return s.cfg.BootstrapToken != "" && subtle.ConstantTimeCompare([]byte(value), []byte(s.cfg.BootstrapToken)) == 1
}
func (s *server) bootstrapStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.checkBootstrap(body.Token) {
		writeJSON(w, 403, map[string]string{"error": "invalid_bootstrap_token"})
		return
	}
	var active int
	_ = s.store.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM users WHERE active=true`).Scan(&active)
	if active > 0 {
		writeJSON(w, 409, map[string]string{"error": "already_bootstrapped"})
		return
	}
	if !auth.ValidUsername(body.Username) || !auth.ValidPassword(body.Password) {
		writeJSON(w, 400, map[string]string{"error": "username_or_password_policy"})
		return
	}
	passwordHash, err := auth.HashPassword(body.Password)
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
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	defer tx.Rollback()
	_, _ = tx.ExecContext(r.Context(), `DELETE FROM users WHERE active=false`)
	_, err = tx.ExecContext(r.Context(), `INSERT INTO users(id,username,password_hash,totp_secret,recovery_hashes,active) VALUES($1,$2,$3,$4,$5,false)`, store.ID("usr"), body.Username, passwordHash, encryptedSecret, store.JSON(hashes))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	if err = tx.Commit(); err != nil {
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
	if !s.checkBootstrap(body.Token) {
		writeJSON(w, 403, map[string]string{"error": "invalid_bootstrap_token"})
		return
	}
	var encryptedSecret string
	err := s.store.DB.QueryRowContext(r.Context(), `SELECT totp_secret FROM users WHERE username=$1 AND active=false`, body.Username).Scan(&encryptedSecret)
	secret, decryptErr := auth.DecryptSecret(s.cfg.EncryptionKey, encryptedSecret)
	if err != nil || decryptErr != nil || !auth.ValidateTOTP(secret, body.TOTP, time.Now()) {
		writeJSON(w, 400, map[string]string{"error": "invalid_totp"})
		return
	}
	_, err = s.store.DB.ExecContext(r.Context(), `UPDATE users SET active=true WHERE username=$1 AND active=false`, body.Username)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	s.store.Audit(r.Context(), "admin.bootstrap.confirm", body.Username, "success", map[string]any{})
	writeJSON(w, 200, map[string]string{"status": "active"})
}

func (s *server) decryptUser(x *user) error {
	secret, err := auth.DecryptSecret(s.cfg.EncryptionKey, x.TOTPSecret)
	if err != nil {
		return err
	}
	x.TOTPSecret = secret
	return nil
}
func (s *server) loadUser(ctx context.Context, username string) (user, error) {
	var x user
	var raw []byte
	err := s.store.DB.QueryRowContext(ctx, `SELECT id,username,password_hash,totp_secret,recovery_hashes FROM users WHERE username=$1 AND active=true`, username).Scan(&x.ID, &x.Username, &x.PasswordHash, &x.TOTPSecret, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &x.Recovery)
	}
	if err == nil {
		err = s.decryptUser(&x)
	}
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
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	key := strings.ToLower(body.Username) + "|" + ip
	if !s.limiter.allow(key) {
		writeJSON(w, 429, map[string]string{"error": "too_many_attempts"})
		return
	}
	x, err := s.loadUser(r.Context(), body.Username)
	ok := err == nil && auth.VerifyPassword(x.PasswordHash, body.Password)
	recoveryIndex := -1
	if ok && !auth.ValidateTOTP(x.TOTPSecret, body.TOTP, time.Now()) {
		hash := auth.HashToken(strings.ToUpper(strings.TrimSpace(body.TOTP)))
		for i, item := range x.Recovery {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(item)) == 1 {
				recoveryIndex = i
				break
			}
		}
		ok = recoveryIndex >= 0
	}
	if !ok {
		s.limiter.fail(key)
		s.store.Audit(r.Context(), "session.login", body.Username, "denied", map[string]any{"ip": ip})
		time.Sleep(250 * time.Millisecond)
		writeJSON(w, 401, map[string]string{"error": "invalid_credentials"})
		return
	}
	if recoveryIndex >= 0 {
		x.Recovery = append(x.Recovery[:recoveryIndex], x.Recovery[recoveryIndex+1:]...)
		_, _ = s.store.DB.ExecContext(r.Context(), `UPDATE users SET recovery_hashes=$2 WHERE id=$1`, x.ID, store.JSON(x.Recovery))
	}
	sessionToken, _ := auth.RandomToken(32)
	csrf, _ := auth.RandomToken(24)
	_, err = s.store.DB.ExecContext(r.Context(), `INSERT INTO sessions(token_hash,user_id,csrf_hash,expires_at) VALUES($1,$2,$3,$4)`, auth.HashToken(sessionToken), x.ID, auth.HashToken(csrf), time.Now().Add(12*time.Hour))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "sg_session", Value: sessionToken, Path: "/", MaxAge: 43200, HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	w.Header().Set("X-CSRF-Token", csrf)
	s.limiter.clear(key)
	s.store.Audit(r.Context(), "session.login", x.Username, "success", map[string]any{"ip": ip})
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
		var recoveryRaw []byte
		var csrfHash string
		var expires time.Time
		err = s.store.DB.QueryRowContext(r.Context(), `SELECT u.id,u.username,u.password_hash,u.totp_secret,u.recovery_hashes,s.csrf_hash,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND u.active=true`, auth.HashToken(cookie.Value)).Scan(&x.ID, &x.Username, &x.PasswordHash, &x.TOTPSecret, &recoveryRaw, &csrfHash, &expires)
		if err == nil {
			err = json.Unmarshal(recoveryRaw, &x.Recovery)
		}
		if err == nil {
			err = s.decryptUser(&x)
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
	cookie, _ := r.Cookie("sg_session")
	if cookie != nil {
		_, _ = s.store.DB.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, auth.HashToken(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "sg_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	s.store.Audit(r.Context(), "session.logout", currentUser(r).Username, "success", map[string]any{})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.LoadOverview(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		value = model.Overview{Node: model.NodeState{Name: s.cfg.NodeName, ServiceState: "unavailable"}, Chain: model.ChainState{Status: "unknown"}, Automation: model.AutomationState{Mode: "observe_only", EligibleAt: s.cfg.InstalledAt.Add(s.cfg.ObserveOnlyPeriod)}, UpdatedAt: time.Now().UTC()}
	} else if err != nil {
		writeJSON(w, 503, map[string]string{"error": "state_unavailable"})
		return
	}
	value.Incidents, _ = s.store.ListIncidents(r.Context(), 8)
	writeJSON(w, 200, value)
}

func metricsRange(value string) (time.Duration, string) {
	switch value {
	case "30m":
		return 30 * time.Minute, "15"
	case "1h":
		return time.Hour, "15"
	case "6h":
		return 6 * time.Hour, "60"
	case "7d":
		return 7 * 24 * time.Hour, "1800"
	case "30d":
		return 30 * 24 * time.Hour, "7200"
	case "24h":
		fallthrough
	default:
		return 24 * time.Hour, "300"
	}
}

func (s *server) metrics(w http.ResponseWriter, r *http.Request) {
	queries := map[string]string{
		"blocks":  "guardian_local_finalized or guardian_external_height",
		"lag":     "guardian_sync_lag",
		"peers":   "guardian_peers",
		"cpu":     "guardian_host_cpu_percent",
		"memory":  "guardian_host_memory_percent",
		"disk":    "guardian_host_disk_percent",
		"rewards": "guardian_collator_reward_sdn_total or guardian_collator_reward_sdn_last or guardian_collator_wallet_free_sdn or guardian_collator_seconds_since_reward",
	}
	query, ok := queries[r.PathValue("panel")]
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "unknown_panel"})
		return
	}
	duration, step := metricsRange(r.URL.Query().Get("range"))
	now := time.Now().UTC()
	values := url.Values{"query": {query}, "start": {now.Add(-duration).Format(time.RFC3339)}, "end": {now.Format(time.RFC3339)}, "step": {step}}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(s.cfg.PrometheusURL, "/")+"/api/v1/query_range?"+values.Encode(), nil)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	response, err := (&http.Client{Timeout: 12 * time.Second}).Do(request)
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "prometheus_unavailable"})
		return
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil || response.StatusCode/100 != 2 {
		writeJSON(w, 503, map[string]string{"error": "prometheus_unavailable"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(raw)
}
func (s *server) rewards(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before_block"), 10, 64)
	summary, err := s.store.LoadRewardOverview(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		summary = model.RewardOverview{Address: s.cfg.RewardAddress, Status: "collecting", Sources: map[string]string{}, Reward24hPlanck: "0", RewardTotalPlanck: "0", LastRewardPlanck: "0"}
	} else if err != nil {
		writeJSON(w, 503, map[string]string{"error": "reward_state_unavailable"})
		return
	}
	items, err := s.store.RewardPage(r.Context(), limit, before)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	daily, err := s.store.RewardDaily(r.Context(), 30)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	next := int64(0)
	if len(items) == limit {
		next = items[len(items)-1].BlockNumber
	}
	writeJSON(w, 200, model.RewardPage{Summary: summary, Items: items, Daily: daily, NextBeforeBlock: next})
}
func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	lines, err := s.agent.Logs(r.Context(), limit, r.URL.Query().Get("since"), r.URL.Query().Get("priority"), r.URL.Query().Get("query"))
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "agent_unavailable"})
		return
	}
	writeJSON(w, 200, map[string]any{"lines": lines})
}
func (s *server) incidents(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListIncidents(r.Context(), 100)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	writeJSON(w, 200, map[string]any{"incidents": items})
}
func (s *server) diagnose(w http.ResponseWriter, r *http.Request) {
	x := currentUser(r)
	id := store.ID("job")
	_, err := s.store.DB.ExecContext(r.Context(), `INSERT INTO jobs(id,kind,payload) VALUES($1,'diagnose_now',$2)`, id, store.JSON(map[string]string{"requested_by": x.Username}))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	s.store.Audit(r.Context(), "diagnosis.request", x.Username, "queued", map[string]string{"job_id": id})
	writeJSON(w, 202, map[string]string{"id": id, "status": "pending"})
}
func (s *server) diagnoses(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.DB.QueryContext(r.Context(), `SELECT payload FROM diagnoses ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	defer rows.Close()
	items := []model.Diagnosis{}
	for rows.Next() {
		var raw []byte
		var item model.Diagnosis
		if rows.Scan(&raw) == nil && json.Unmarshal(raw, &item) == nil {
			items = append(items, item)
		}
	}
	writeJSON(w, 200, map[string]any{"diagnoses": items})
}
func (s *server) enqueueTest(w http.ResponseWriter, r *http.Request, kind string) {
	x := currentUser(r)
	id := store.ID("job")
	_, err := s.store.DB.ExecContext(r.Context(), `INSERT INTO jobs(id,kind,payload) VALUES($1,$2,$3)`, id, kind, store.JSON(map[string]string{"requested_by": x.Username}))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	s.store.Audit(r.Context(), "integration."+kind, x.Username, "queued", map[string]string{"job_id": id})
	writeJSON(w, 202, map[string]string{"id": id, "status": "pending"})
}
func (s *server) testSMTP(w http.ResponseWriter, r *http.Request) { s.enqueueTest(w, r, "test_smtp") }
func (s *server) testGemini(w http.ResponseWriter, r *http.Request) {
	s.enqueueTest(w, r, "test_gemini")
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
	if body.Confirm != s.cfg.NodeName || len(strings.TrimSpace(body.Reason)) < 10 || !auth.VerifyPassword(x.PasswordHash, body.Password) || !auth.ValidateTOTP(x.TOTPSecret, body.TOTP, time.Now()) {
		s.store.Audit(r.Context(), "restart.request", x.Username, "denied", map[string]any{})
		writeJSON(w, 403, map[string]string{"error": "step_up_failed"})
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 100 {
		writeJSON(w, 400, map[string]string{"error": "idempotency_key_required"})
		return
	}
	id := store.ID("act")
	err := s.store.DB.QueryRowContext(r.Context(), `INSERT INTO remediation_actions(id,idempotency_key,incident_id,requested_by,reason,mode,status) VALUES($1,$2,NULLIF($3,''),$4,$5,'manual','pending') ON CONFLICT(idempotency_key) DO UPDATE SET idempotency_key=excluded.idempotency_key RETURNING id`, id, key, body.IncidentID, x.Username, strings.TrimSpace(body.Reason)).Scan(&id)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	s.store.Audit(r.Context(), "restart.request", x.Username, "queued", map[string]string{"action_id": id})
	writeJSON(w, 202, map[string]string{"id": id, "status": "pending"})
}
func (s *server) settings(w http.ResponseWriter, r *http.Request) {
	enabled := s.store.SettingBool(r.Context(), "automation_enabled", false)
	writeJSON(w, 200, map[string]any{"automation_enabled": enabled, "observe_until": s.cfg.InstalledAt.Add(s.cfg.ObserveOnlyPeriod), "node_name": s.cfg.NodeName, "systemd_unit": s.cfg.SystemdUnit})
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
	if !auth.VerifyPassword(x.PasswordHash, body.Password) || !auth.ValidateTOTP(x.TOTPSecret, body.TOTP, time.Now()) {
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
func (s *server) audit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	events, err := s.store.ListAudit(r.Context(), limit)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database"})
		return
	}
	writeJSON(w, 200, map[string]any{"events": events})
}
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]string{"error": "stream_unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		fmt.Fprintf(w, "event: heartbeat\ndata: {\"time\":%q}\n\n", time.Now().UTC().Format(time.RFC3339))
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

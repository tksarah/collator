package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
	"github.com/shiden-guardian/shiden-guardian/internal/reward"
)

type config struct {
	Unit, NodeName, ObserveSocket, ControlSocket, StatePath, LockPath, RestartWrapper string
	RewardAddress, RewardRPC                                                          string
	MetricsURLs                                                                       []string
}
type agent struct {
	cfg    config
	mu     sync.Mutex
	reward *reward.Monitor
}
type guardState struct {
	Attempts    []time.Time            `json:"attempts"`
	Processed   map[string]guardResult `json:"processed"`
	LastFailure time.Time              `json:"last_failure,omitempty"`
}
type guardResult struct {
	Status  string    `json:"status"`
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

func main() {
	cfg := config{Unit: getenv("SYSTEMD_UNIT", "astar.service"), NodeName: getenv("NODE_NAME", "tk_sdn_collator"), ObserveSocket: getenv("OBSERVE_SOCKET", "/run/shiden-guardian/observe/agent.sock"), ControlSocket: getenv("CONTROL_SOCKET", "/run/shiden-guardian/control/agent.sock"), StatePath: getenv("STATE_PATH", "/var/lib/shiden-guardian/guard.json"), LockPath: getenv("LOCK_PATH", "/etc/shiden-guardian/automation-disabled"), RestartWrapper: getenv("RESTART_WRAPPER", "/usr/local/libexec/shiden-guardian-restart"), RewardAddress: getenv("COLLATOR_REWARD_ADDRESS", model.RewardWallet), RewardRPC: getenv("PARACHAIN_RPC_URL", "http://127.0.0.1:9944"), MetricsURLs: []string{getenv("PARACHAIN_METRICS_URL", "http://127.0.0.1:9615/metrics"), getenv("RELAY_METRICS_URL", "http://127.0.0.1:9616/metrics")}}
	rewardMonitor, rewardErr := reward.NewMonitor(cfg.RewardAddress, "local", reward.NewHTTPRPC(cfg.RewardRPC))
	if rewardErr != nil {
		slog.Error("reward monitor configuration", "error", rewardErr)
	}
	a := &agent{cfg: cfg, reward: rewardMonitor}
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- a.serve(cfg.ObserveSocket, false) }()
	go func() { errorsCh <- a.serve(cfg.ControlSocket, true) }()
	if err := <-errorsCh; err != nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}
func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func (a *agent) serve(path string, control bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(path, 0660); err != nil {
		return err
	}
	mux := http.NewServeMux()
	if control {
		mux.HandleFunc("POST /v1/restart", a.restart)
	} else {
		mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
		mux.HandleFunc("GET /v1/snapshot", a.snapshot)
		mux.HandleFunc("GET /v1/metrics", a.metrics)
		mux.HandleFunc("GET /v1/logs", a.logs)
		mux.HandleFunc("GET /v1/rewards/snapshot", a.rewardSnapshot)
		mux.HandleFunc("GET /v1/rewards/scan", a.rewardScan)
	}
	writeTimeout := 3 * time.Minute
	if control {
		writeTimeout = 13 * time.Minute
	}
	server := http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: writeTimeout, MaxHeaderBytes: 64 << 10}
	return server.Serve(listener)
}

func (a *agent) rewardSnapshot(w http.ResponseWriter, r *http.Request) {
	if a.reward == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reward_monitor_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	snapshot, err := a.reward.Snapshot(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reward_rpc_unavailable", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (a *agent) rewardScan(w http.ResponseWriter, r *http.Request) {
	if a.reward == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reward_monitor_unavailable"})
		return
	}
	from, fromErr := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, toErr := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if fromErr != nil || toErr != nil || from <= 0 || to < from || to-from+1 > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_range"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	scan, err := a.reward.Scan(ctx, from, to)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reward_scan_failed", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, scan)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (a *agent) snapshot(w http.ResponseWriter, r *http.Request) {
	node, err := a.serviceState(r.Context())
	if err != nil {
		node = model.NodeState{Name: a.cfg.NodeName, ServiceState: "unavailable"}
	}
	host := hostState()
	_, lockErr := os.Stat(a.cfg.LockPath)
	ok := true
	for _, endpoint := range a.cfg.MetricsURLs {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		_, err := fetch(ctx, endpoint)
		cancel()
		if err != nil {
			ok = false
		}
	}
	writeJSON(w, 200, model.AgentSnapshot{Node: node, Host: host, MetricsOK: ok, AutomationLocked: lockErr == nil, UpdatedAt: time.Now().UTC()})
}
func (a *agent) serviceState(ctx context.Context) (model.NodeState, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "systemctl", "show", a.cfg.Unit, "--no-pager", "--property=ActiveState,NRestarts,ActiveEnterTimestampMonotonic,ExecStart")
	raw, err := command.Output()
	if err != nil {
		return model.NodeState{}, err
	}
	props := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if found {
			props[key] = value
		}
	}
	restarts, _ := strconv.ParseInt(props["NRestarts"], 10, 64)
	entered, _ := strconv.ParseInt(props["ActiveEnterTimestampMonotonic"], 10, 64)
	uptime := int64(0)
	if entered > 0 {
		if raw, err := os.ReadFile("/proc/uptime"); err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) > 0 {
				bootSeconds, _ := strconv.ParseFloat(fields[0], 64)
				uptime = max(0, int64(bootSeconds)-entered/1_000_000)
			}
		}
	}
	version := binaryVersion(ctx, props["ExecStart"])
	return model.NodeState{Name: a.cfg.NodeName, ServiceState: props["ActiveState"], Version: version, Uptime: uptime, RestartCount: restarts}, nil
}
func binaryVersion(parent context.Context, execStart string) string {
	match := regexp.MustCompile(`path=([^ ;]+astar-collator)`).FindStringSubmatch(execStart)
	if len(match) != 2 {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, match[1], "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(raw))
}

func hostState() model.HostState {
	return model.HostState{CPU: cpuPercent(), Memory: memoryPercent(), Disk: diskPercent("/"), Temperature: temperature()}
}
func cpuPercent() float64 {
	idle1, total1 := cpuTimes()
	time.Sleep(150 * time.Millisecond)
	idle2, total2 := cpuTimes()
	if total2 <= total1 {
		return 0
	}
	return clamp(100 * (1 - float64(idle2-idle1)/float64(total2-total1)))
}
func cpuTimes() (uint64, uint64) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	fields := strings.Fields(strings.SplitN(string(raw), "\n", 2)[0])
	var values []uint64
	for _, field := range fields[1:] {
		value, _ := strconv.ParseUint(field, 10, 64)
		values = append(values, value)
	}
	var total uint64
	for _, v := range values {
		total += v
	}
	var idle uint64
	if len(values) > 3 {
		idle = values[3]
	}
	if len(values) > 4 {
		idle += values[4]
	}
	return idle, total
}
func memoryPercent() float64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	values := map[string]float64{}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			values[strings.TrimSuffix(fields[0], ":")], _ = strconv.ParseFloat(fields[1], 64)
		}
	}
	if values["MemTotal"] == 0 {
		return 0
	}
	return clamp(100 * (1 - values["MemAvailable"]/values["MemTotal"]))
}
func temperature() float64 {
	files, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	var values []float64
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err == nil {
			value, _ := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
			if value > 1000 {
				value /= 1000
			}
			if value > 0 && value < 150 {
				values = append(values, value)
			}
		}
	}
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	return values[len(values)-1]
}
func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func (a *agent) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for i, endpoint := range a.cfg.MetricsURLs {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		raw, err := fetch(ctx, endpoint)
		cancel()
		if err != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		fmt.Fprintf(w, "# guardian source %d\n", i)
		_, _ = w.Write(raw)
		_, _ = w.Write([]byte("\n"))
	}
}
func fetch(ctx context.Context, endpoint string) ([]byte, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	client := http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("metrics %s", response.Status)
	}
	return io.ReadAll(io.LimitReader(response.Body, 16<<20))
}

var allowedSince = regexp.MustCompile(`^(?:\d{4}-\d{2}-\d{2}T[0-9:.+-]+Z?|\d{1,4} (?:minute|minutes|hour|hours|day|days) ago)$`)

func (a *agent) logs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	args := []string{"--no-pager", "--unit", a.cfg.Unit, "--output=json", "--lines", strconv.Itoa(limit)}
	since := r.URL.Query().Get("since")
	if since != "" {
		if !allowedSince.MatchString(since) {
			writeJSON(w, 400, map[string]string{"error": "invalid_since"})
			return
		}
		args = append(args, "--since", since)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "journalctl", args...).Output()
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "journal_unavailable"})
		return
	}
	query := strings.ToLower(r.URL.Query().Get("query"))
	minPriority := priorityNumber(r.URL.Query().Get("priority"))
	lines := []model.LogLine{}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 2<<20)
	for scanner.Scan() {
		var entry map[string]any
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		message := stringValue(entry["MESSAGE"])
		if query != "" && !strings.Contains(strings.ToLower(message), query) {
			continue
		}
		priority, _ := strconv.Atoi(stringValue(entry["PRIORITY"]))
		if minPriority >= 0 && priority > minPriority {
			continue
		}
		micros, _ := strconv.ParseInt(stringValue(entry["__REALTIME_TIMESTAMP"]), 10, 64)
		lines = append(lines, model.LogLine{Cursor: stringValue(entry["__CURSOR"]), Timestamp: time.UnixMicro(micros).UTC(), Priority: priorityName(priority), Message: message})
	}
	writeJSON(w, 200, map[string]any{"lines": lines})
}
func stringValue(value any) string {
	switch x := value.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}
func priorityNumber(value string) int {
	switch strings.ToLower(value) {
	case "emerg":
		return 0
	case "alert":
		return 1
	case "crit", "critical":
		return 2
	case "err", "error":
		return 3
	case "warning", "warn":
		return 4
	case "notice":
		return 5
	case "info":
		return 6
	case "debug":
		return 7
	default:
		return -1
	}
}
func priorityName(value int) string {
	switch value {
	case 0, 1, 2:
		return "critical"
	case 3:
		return "error"
	case 4:
		return "warning"
	case 5:
		return "notice"
	case 7:
		return "debug"
	default:
		return "info"
	}
}

var actionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{5,100}$`)

func (a *agent) restart(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		ActionID string `json:"action_id"`
		Reason   string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || !actionPattern.MatchString(body.ActionID) || len(strings.TrimSpace(body.Reason)) < 10 || len(body.Reason) > 500 {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	if _, err := os.Stat(a.cfg.LockPath); err == nil {
		writeJSON(w, 423, map[string]string{"error": "automation_locked"})
		return
	}
	state := a.loadGuard()
	if result, ok := state.Processed[body.ActionID]; ok {
		writeJSON(w, 200, map[string]any{"status": result.Status, "message": result.Message, "idempotent": true})
		return
	}
	cut := time.Now().Add(-24 * time.Hour)
	attempts := state.Attempts[:0]
	for _, attempt := range state.Attempts {
		if attempt.After(cut) {
			attempts = append(attempts, attempt)
		}
	}
	state.Attempts = attempts
	if len(state.Attempts) >= 2 {
		writeJSON(w, 429, map[string]string{"error": "daily_limit"})
		return
	}
	if len(state.Attempts) > 0 && time.Since(state.Attempts[len(state.Attempts)-1]) < time.Hour {
		writeJSON(w, 429, map[string]string{"error": "cooldown"})
		return
	}
	if !state.LastFailure.IsZero() {
		writeJSON(w, 423, map[string]string{"error": "previous_recovery_failed"})
		return
	}
	state.Attempts = append(state.Attempts, time.Now().UTC())
	if state.Processed == nil {
		state.Processed = map[string]guardResult{}
	}
	state.Processed[body.ActionID] = guardResult{Status: "running", At: time.Now().UTC(), Message: "restart requested"}
	_ = a.saveGuard(state)
	baseline := a.finalizedBlock(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "sudo", "-n", a.cfg.RestartWrapper)
	output, err := command.CombinedOutput()
	message := strings.TrimSpace(string(output))
	status := "succeeded"
	if err == nil {
		err = a.waitActive(ctx)
	}
	if err == nil {
		err = a.waitBlockProgress(ctx, baseline)
	}
	if err != nil {
		status = "failed"
		state.LastFailure = time.Now().UTC()
		message = err.Error() + ": " + message
	}
	state.Processed[body.ActionID] = guardResult{Status: status, At: time.Now().UTC(), Message: message}
	_ = a.saveGuard(state)
	if err != nil {
		writeJSON(w, 500, map[string]any{"status": status, "error": message})
		return
	}
	writeJSON(w, 200, map[string]any{"status": status, "message": message})
}
func (a *agent) finalizedBlock(ctx context.Context) int64 {
	if len(a.cfg.MetricsURLs) == 0 {
		return 0
	}
	readCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	raw, err := fetch(readCtx, a.cfg.MetricsURLs[0])
	if err != nil {
		return 0
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "substrate_block_height") || !strings.Contains(line, `status="finalized"`) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseInt(strings.Split(fields[1], ".")[0], 10, 64)
		if err == nil {
			return value
		}
	}
	return 0
}
func (a *agent) waitBlockProgress(ctx context.Context, baseline int64) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if current := a.finalizedBlock(ctx); current > baseline {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("finalized block did not advance after restart: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
func (a *agent) waitActive(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		state, err := a.serviceState(ctx)
		if err == nil && state.ServiceState == "active" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("service did not become active: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
func (a *agent) loadGuard() guardState {
	state := guardState{Processed: map[string]guardResult{}}
	raw, err := os.ReadFile(a.cfg.StatePath)
	if err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	if state.Processed == nil {
		state.Processed = map[string]guardResult{}
	}
	stale := false
	for id, result := range state.Processed {
		if result.Status == "running" {
			result.Status = "failed"
			result.Message = "agent restarted before recovery verification completed"
			result.At = time.Now().UTC()
			state.Processed[id] = result
			state.LastFailure = result.At
			stale = true
		}
	}
	if stale {
		_ = a.saveGuard(state)
	}
	return state
}
func (a *agent) saveGuard(state guardState) error {
	if err := os.MkdirAll(filepath.Dir(a.cfg.StatePath), 0750); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(state, "", "  ")
	temp := a.cfg.StatePath + ".tmp"
	if err := os.WriteFile(temp, raw, 0640); err != nil {
		return err
	}
	return os.Rename(temp, a.cfg.StatePath)
}

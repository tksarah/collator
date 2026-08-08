package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/actionbroker"
	"github.com/shiden-guardian/shiden-guardian/internal/agentclient"
	"github.com/shiden-guardian/shiden-guardian/internal/config"
	"github.com/shiden-guardian/shiden-guardian/internal/gemini"
	"github.com/shiden-guardian/shiden-guardian/internal/model"
	"github.com/shiden-guardian/shiden-guardian/internal/notify"
	"github.com/shiden-guardian/shiden-guardian/internal/reward"
	"github.com/shiden-guardian/shiden-guardian/internal/rules"
	"github.com/shiden-guardian/shiden-guardian/internal/store"
)

type controller struct {
	cfg                 config.Config
	store               *store.Store
	agent               *agentclient.Client
	gemini              *gemini.Client
	mail                notify.SMTP
	evaluator           rules.Evaluator
	mu                  sync.RWMutex
	current             model.Overview
	rawMetrics          string
	external            [2]int64
	externalOK          [2]bool
	lastExternal        time.Time
	autoSeen            map[string]time.Time
	smtpHealthy         bool
	actionMu            sync.Mutex
	actionVerifier      *actionbroker.Verifier
	lastCleanup         time.Time
	rewardExternal      []*reward.Monitor
	rewardExternalState [2]model.RewardSnapshot
	rewardExternalOK    [2]bool
	rewardExternalRetry [2]time.Time
	rewardExternalFails [2]int
	rewardChainHeight   [2]int64
	rewardChainProgress [2]time.Time
	rewardScanner       rewardHistoryScanner
	rewardWriter        rewardHistoryWriter
	rewardScanNext      time.Time
	rewardScanFailures  int
	rewardMu            sync.Mutex
}

const (
	rewardScanChunkSize    = int64(16)
	rewardScanMaxChunkSize = store.RewardScanMaxTransitionBlocks
	rewardScanInterval     = 15 * time.Second
)

type rewardHistoryScanner interface {
	RewardScan(context.Context, int64, int64) (model.RewardScan, error)
}

type rewardHistoryWriter interface {
	ApplyRewardScan(context.Context, int64, model.RewardOverview, []model.RewardObservation) error
}

type rewardGapAuthority interface {
	RewardSnapshot(context.Context) (model.RewardSnapshot, error)
	RewardScan(context.Context, int64, int64) (model.RewardScan, error)
}

type rewardGapWriter interface {
	AcknowledgePrunedRewardGap(context.Context, int64, int64, int64, store.RewardGapApproval) (store.RewardGapRebase, error)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		connection, err := net.DialTimeout("tcp", "127.0.0.1:9091", 3*time.Second)
		if err != nil {
			os.Exit(1)
		}
		connection.Close()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "-acknowledge-pruned-reward-gap" {
		if err := runPrunedRewardGapAcknowledgement(os.Args[2:]); err != nil {
			slog.Error("pruned reward gap acknowledgement", "error", err)
			os.Exit(1)
		}
		return
	}
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	key, err := actionbroker.DecodeKey(cfg.ActionBrokerKey)
	if err != nil {
		slog.Error("action broker key", "error", err)
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
	c := &controller{cfg: cfg, store: db, agent: agentclient.New(cfg.AgentObserveSock, cfg.AgentControlSock), gemini: gemini.New(cfg.GeminiAPIKey, cfg.GeminiModel), mail: notify.SMTP{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Password: cfg.SMTPPassword, From: cfg.SMTPFrom, To: cfg.SMTPTo, UnixSocket: cfg.SMTPUnixSocket, TLSMode: cfg.SMTPTLSMode}, autoSeen: map[string]time.Time{}, actionVerifier: actionbroker.NewVerifier(key)}
	c.rewardScanner = c.agent
	c.rewardWriter = c.store
	for i, endpoint := range cfg.ExternalRPC {
		if i >= 2 {
			break
		}
		monitor, monitorErr := reward.NewMonitor(cfg.RewardAddress, fmt.Sprintf("external_%d", i+1), reward.NewHTTPRPC(endpoint))
		if monitorErr == nil {
			c.rewardExternal = append(c.rewardExternal, monitor)
		}
	}
	_, _ = db.DB.ExecContext(context.Background(), `UPDATE jobs SET status='pending' WHERE status='running' AND created_at < now() - interval '15 minutes'`)
	_, _ = db.DB.ExecContext(context.Background(), `UPDATE remediation_actions SET status='pending',started_at=NULL WHERE status='running' AND started_at < now() - interval '15 minutes'`)
	if err := c.startActionBroker(); err != nil {
		slog.Error("action broker", "error", err)
		os.Exit(1)
	}
	go c.serveMetrics()
	go c.loop()
	select {}
}

func runPrunedRewardGapAcknowledgement(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: controller -acknowledge-pruned-reward-gap EXPECTED_CURSOR")
	}
	expectedCursor, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || expectedCursor <= 0 {
		return fmt.Errorf("invalid expected reward cursor")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.DB.Close()
	result, err := acknowledgePrunedRewardGap(ctx, agentclient.New(cfg.AgentObserveSock, cfg.AgentControlSock), db, expectedCursor, store.RewardGapApproval{Actor: "operator-approved-cli", Reason: "operator approved local state pruning recovery"})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func acknowledgePrunedRewardGap(ctx context.Context, authority rewardGapAuthority, writer rewardGapWriter, expectedCursor int64, approval store.RewardGapApproval) (store.RewardGapRebase, error) {
	if expectedCursor <= 0 {
		return store.RewardGapRebase{}, fmt.Errorf("invalid expected reward cursor")
	}
	snapshot, err := authority.RewardSnapshot(ctx)
	if err != nil {
		return store.RewardGapRebase{}, fmt.Errorf("local finalized reward snapshot: %w", err)
	}
	if snapshot.Source != "local" || !snapshot.SchemaOK || snapshot.FinalizedBlock <= 0 || snapshot.FinalizedHash == "" {
		return store.RewardGapRebase{}, fmt.Errorf("invalid local finalized reward snapshot")
	}
	if snapshot.FinalizedBlock-expectedCursor <= store.RewardGapRetainedWindowBlocks {
		return store.RewardGapRebase{}, fmt.Errorf("reward cursor is still within the retained recovery window")
	}
	discardedTo := min(expectedCursor+rewardScanChunkSize, snapshot.FinalizedBlock)
	if _, discardedErr := authority.RewardScan(ctx, expectedCursor+1, discardedTo); discardedErr == nil {
		return store.RewardGapRebase{}, fmt.Errorf("reward history is readable; acknowledgement is not permitted")
	} else if !isPrunedRewardStateError(discardedErr) {
		return store.RewardGapRebase{}, fmt.Errorf("reward history failed for a reason other than local state pruning: %w", discardedErr)
	}
	baseline := snapshot.FinalizedBlock - store.RewardGapRetainedWindowBlocks
	probeFrom := baseline + 1
	probeTo := min(probeFrom+rewardScanChunkSize-1, snapshot.FinalizedBlock)
	probe, err := authority.RewardScan(ctx, probeFrom, probeTo)
	if err != nil {
		return store.RewardGapRebase{}, fmt.Errorf("retained local reward scan: %w", err)
	}
	if err := validateLocalRewardScan(probe, probeFrom, probeTo); err != nil {
		return store.RewardGapRebase{}, fmt.Errorf("retained local reward scan validation: %w", err)
	}
	return writer.AcknowledgePrunedRewardGap(ctx, expectedCursor, baseline, snapshot.FinalizedBlock, approval)
}

func isPrunedRewardStateError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "state already discarded")
}

func rewardRecoveryForScanError(cursor, finalized int64, err error) *model.RewardRecovery {
	if !isPrunedRewardStateError(err) || cursor <= 0 || finalized-cursor <= store.RewardGapRetainedWindowBlocks {
		return nil
	}
	return &model.RewardRecovery{
		Required:                 true,
		Reason:                   "local_state_pruned",
		ExpectedCursor:           cursor,
		CandidateResumeFromBlock: finalized - store.RewardGapRetainedWindowBlocks + 1,
	}
}

func (c *controller) loop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		c.collect(ctx)
		c.processJobs(ctx)
		cancel()
		go c.processPendingAction()
		<-ticker.C
	}
}
func (c *controller) collect(ctx context.Context) {
	if time.Since(c.lastExternal) >= time.Minute {
		c.updateExternal(ctx)
	}
	snapshot, agentErr := c.agent.Snapshot(ctx)
	metrics, metricsErr := c.agent.Metrics(ctx)
	state := model.Overview{Node: model.NodeState{Name: c.cfg.NodeName, ServiceState: "unavailable"}, Chain: model.ChainState{Status: "unknown", ExternalA: c.external[0], ExternalB: c.external[1]}, Automation: model.AutomationState{Mode: "observe_only", EligibleAt: c.cfg.InstalledAt.Add(c.cfg.ObserveOnlyPeriod)}, UpdatedAt: time.Now().UTC()}
	if agentErr == nil {
		state.Node = snapshot.Node
		state.Host = snapshot.Host
		state.Automation.HostLocked = snapshot.AutomationLocked
	}
	if metricsErr == nil {
		state.Chain.LocalBest = int64(metric(metrics, []string{"substrate_block_height"}, `status="best"`))
		state.Chain.LocalFinalized = int64(metric(metrics, []string{"substrate_block_height"}, `status="finalized"`))
		state.Chain.Peers = int64(metric(metrics, []string{"substrate_sub_libp2p_peers_count", "substrate_peers_count"}, ""))
		c.rawMetrics = metrics
	}
	state.Chain.ExternalHeight = max64(c.external[0], c.external[1])
	if state.Chain.LocalFinalized > 0 && state.Chain.ExternalHeight > 0 {
		state.Chain.Lag = max64(0, state.Chain.ExternalHeight-state.Chain.LocalFinalized)
	}
	if state.Node.ServiceState == "active" && metricsErr == nil && state.Chain.Lag < 30 {
		state.Chain.Status = "healthy"
	} else if state.Node.ServiceState != "active" {
		state.Chain.Status = "critical"
	} else {
		state.Chain.Status = "warning"
	}
	state.Automation.Enabled = c.store.SettingBool(ctx, "automation_enabled", false)
	if state.Automation.Enabled {
		state.Automation.Mode = "enabled"
	} else if time.Now().Before(state.Automation.EligibleAt) {
		state.Automation.Mode = "observe_only"
	} else {
		state.Automation.Mode = "manual"
	}
	rewardSignals := c.collectRewards(ctx, &state)
	signals := append(c.evaluator.Evaluate(time.Now(), state), rewardSignals...)
	if recentLogs, logErr := c.agent.Logs(ctx, 200, "5 minutes ago", "warning", ""); logErr == nil {
		signals = append(signals, logSignals(recentLogs)...)
	}
	c.handleSignals(ctx, state, signals, agentErr, metricsErr)
	state.Incidents, _ = c.store.ListIncidents(ctx, 8)
	_ = c.store.SaveOverview(ctx, state)
	c.cleanupRetention(ctx)
	c.mu.Lock()
	c.current = state
	c.mu.Unlock()
}
func logSignals(logs []model.LogLine) []rules.Signal {
	patterns := []struct {
		fingerprint, title string
		terms              []string
	}{
		{"log-oom", "OOM / メモリ強制終了を検知", []string{"out of memory", "oom-kill", "oom kill"}},
		{"log-panic", "astar.service のpanicを検知", []string{"panicked at", "thread 'main' panicked", " panic:"}},
		{"log-database", "チェーンDBエラーを検知", []string{"database error", "db error", "corruption", "corrupted database"}},
	}
	seen := map[string]bool{}
	out := []rules.Signal{}
	for _, line := range logs {
		message := strings.ToLower(line.Message)
		for _, pattern := range patterns {
			if seen[pattern.fingerprint] {
				continue
			}
			for _, term := range pattern.terms {
				if strings.Contains(message, term) {
					seen[pattern.fingerprint] = true
					out = append(out, rules.Signal{Fingerprint: pattern.fingerprint, Severity: "critical", Title: pattern.title, AutoCandidate: false, Evidence: map[string]any{"log_cursor": line.Cursor, "timestamp": line.Timestamp}})
					break
				}
			}
		}
	}
	return out
}
func (c *controller) cleanupRetention(ctx context.Context) {
	if time.Since(c.lastCleanup) < 24*time.Hour {
		return
	}
	c.lastCleanup = time.Now()
	_, _ = c.store.DB.ExecContext(ctx, `DELETE FROM jobs WHERE finished_at < now() - interval '30 days'`)
	_, _ = c.store.DB.ExecContext(ctx, `DELETE FROM audit_events WHERE created_at < now() - interval '365 days'`)
	_, _ = c.store.DB.ExecContext(ctx, `DELETE FROM remediation_actions WHERE created_at < now() - interval '365 days'`)
	_, _ = c.store.DB.ExecContext(ctx, `DELETE FROM incidents WHERE status='resolved' AND resolved_at < now() - interval '365 days'`)
	_, _ = c.store.DB.ExecContext(ctx, `DELETE FROM reward_events WHERE authored_at < now() - interval '365 days'`)
}

func (c *controller) collectRewards(ctx context.Context, state *model.Overview) []rules.Signal {
	c.rewardMu.Lock()
	defer c.rewardMu.Unlock()
	now := time.Now().UTC()
	for i := range c.external {
		if c.externalOK[i] && c.external[i] > c.rewardChainHeight[i] {
			c.rewardChainHeight[i] = c.external[i]
			c.rewardChainProgress[i] = now
		}
	}
	local, localErr := c.agent.RewardSnapshot(ctx)
	previous, stateErr := c.store.LoadRewardOverview(ctx)
	if errors.Is(stateErr, sql.ErrNoRows) {
		previous = model.RewardOverview{Address: c.cfg.RewardAddress, Status: "collecting", MonitoringStartedAt: now, Sources: map[string]string{}, Reward24hPlanck: "0", RewardTotalPlanck: "0", LastRewardPlanck: "0"}
		if localErr == nil {
			previous.LastScannedBlock = local.FinalizedBlock
			previous.LastAuthoredBlock = local.LastAuthoredBlock
		}
	} else if stateErr != nil {
		state.Rewards = model.RewardOverview{Address: c.cfg.RewardAddress, Status: "unknown", Sources: map[string]string{"database": "unavailable"}, Gap: stateErr.Error()}
		return []rules.Signal{rewardSignal("reward-monitor-degraded", "warning", "報酬監視データベースを利用できません", map[string]any{"error": stateErr.Error()})}
	}
	if previous.Sources == nil {
		previous.Sources = map[string]string{}
	}
	previous.Address = c.cfg.RewardAddress
	if localErr != nil {
		previous.Sources["local"] = "unavailable"
		previous.Gap = localErr.Error()
		previous.Recovery = nil
	} else {
		if local.SchemaOK {
			previous.Sources["local"] = "ok"
		} else {
			previous.Sources["local"] = "unsupported_runtime"
		}
		previous.FinalizedBlock = local.FinalizedBlock
		previous.LastAuthoredBlock = local.LastAuthoredBlock
		previous.WalletFreePlanck = local.WalletFreePlanck
		previous.ActiveSession = local.ActiveSession
		previous.ValidatorCount = local.ValidatorCount
		previous.SpecVersion = local.SpecVersion
		previous.SchemaOK = local.SchemaOK
	}

	for i := range c.rewardExternal {
		if !c.rewardExternalReady(i, now) {
			continue
		}
		if c.rewardExternalOK[i] && now.Sub(c.rewardExternalState[i].ObservedAt) < time.Minute {
			continue
		}
		externalCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		snapshot, err := c.rewardExternal[i].Snapshot(externalCtx)
		cancel()
		c.rewardExternalOK[i] = err == nil
		if err == nil {
			c.recordRewardExternalSuccess(i, now)
			c.rewardExternalState[i] = snapshot
			if snapshot.SchemaOK {
				previous.Sources[fmt.Sprintf("external_%d", i+1)] = "ok"
			} else {
				previous.Sources[fmt.Sprintf("external_%d", i+1)] = "unsupported_runtime"
			}
		} else {
			c.recordRewardExternalFailure(i, now)
			previous.Sources[fmt.Sprintf("external_%d", i+1)] = "unavailable"
		}
	}
	for i := range c.rewardExternal {
		if !c.rewardExternalOK[i] {
			previous.Sources[fmt.Sprintf("external_%d", i+1)] = "unavailable"
		}
	}
	quorum := 0
	if localErr == nil && local.SchemaOK {
		quorum++
	}
	for i := range c.rewardExternal {
		if c.rewardExternalOK[i] && c.rewardExternalState[i].SchemaOK && c.rewardExternalState[i].ActiveSession == previous.ActiveSession && c.rewardExternalState[i].Address == previous.Address {
			quorum++
		}
	}
	previous.Quorum = quorum
	if quorum >= 2 && !previous.ActiveSession {
		previous.InactiveConfirmations++
	} else {
		previous.InactiveConfirmations = 0
	}

	if localErr == nil && previous.LastScannedBlock > 0 && local.FinalizedBlock > previous.LastScannedBlock {
		updated, attempted, scanErr := c.scanLocalRewardHistory(ctx, now, previous, local.FinalizedBlock)
		if scanErr != nil {
			cursor := previous.LastScannedBlock + 1
			end := rewardScanEnd(previous.LastScannedBlock, local.FinalizedBlock)
			previous.Gap = fmt.Sprintf("blocks %d-%d: %v", cursor, end, scanErr)
			previous.Recovery = rewardRecoveryForScanError(previous.LastScannedBlock, local.FinalizedBlock, scanErr)
		} else if attempted {
			previous = updated
		}
	} else if localErr == nil && local.FinalizedBlock < previous.LastScannedBlock {
		previous.Gap = fmt.Sprintf("local finalized block %d is behind reward cursor %d", local.FinalizedBlock, previous.LastScannedBlock)
		previous.Recovery = nil
	} else if localErr == nil {
		previous.Gap = ""
		previous.Recovery = nil
	}
	if localErr == nil && previous.LastScannedBlock == 0 {
		previous.LastScannedBlock = local.FinalizedBlock
	}
	_ = c.store.RewardStats(ctx, &previous)
	if !previous.LastRewardAt.IsZero() {
		previous.SecondsSinceReward = max64(0, int64(now.Sub(previous.LastRewardAt).Seconds()))
	} else if !previous.MonitoringStartedAt.IsZero() {
		previous.SecondsSinceReward = max64(0, int64(now.Sub(previous.MonitoringStartedAt).Seconds()))
	}
	previous.BlocksSinceAuthored = max64(0, previous.FinalizedBlock-previous.LastAuthoredBlock)
	previous.KickBlocksRemaining = max64(0, reward.KickThresholdBlocks-previous.BlocksSinceAuthored)
	previous.Status = rewardStatus(previous, c.cfg.RewardWarnAfter, c.cfg.RewardCriticalAfter)
	_ = c.store.SaveRewardOverview(ctx, previous)
	state.Rewards = previous
	return c.rewardSignals(ctx, previous, *state)
}

func rewardRetryBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	backoff := time.Minute * time.Duration(1<<min(failures-1, 4))
	if backoff > 15*time.Minute {
		return 15 * time.Minute
	}
	return backoff
}

func rewardScanRetryBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	backoff := rewardScanInterval * time.Duration(1<<min(failures-1, 5))
	if backoff > 5*time.Minute {
		return 5 * time.Minute
	}
	return backoff
}

func (c *controller) rewardExternalReady(index int, now time.Time) bool {
	return index >= 0 && index < len(c.rewardExternalRetry) && (c.rewardExternalRetry[index].IsZero() || !now.Before(c.rewardExternalRetry[index]))
}

func (c *controller) recordRewardExternalSuccess(index int, now time.Time) {
	if index < 0 || index >= len(c.rewardExternalRetry) {
		return
	}
	c.rewardExternalOK[index] = true
	c.rewardExternalFails[index] = 0
	c.rewardExternalRetry[index] = now.Add(time.Minute)
}

func (c *controller) recordRewardExternalFailure(index int, now time.Time) {
	if index < 0 || index >= len(c.rewardExternalRetry) {
		return
	}
	c.rewardExternalOK[index] = false
	c.rewardExternalFails[index]++
	c.rewardExternalRetry[index] = now.Add(rewardRetryBackoff(c.rewardExternalFails[index]))
}

func validateLocalRewardScan(scan model.RewardScan, from, to int64) error {
	if scan.Source != "local" || scan.From != from || scan.To != to {
		return fmt.Errorf("invalid local reward scan envelope")
	}
	seen := make(map[int64]struct{}, len(scan.Observations))
	for _, observation := range scan.Observations {
		if observation.BlockNumber < from || observation.BlockNumber > to || observation.BlockHash == "" || observation.SourceCount != 1 {
			return fmt.Errorf("invalid local reward observation")
		}
		switch observation.Verification {
		case "confirmed", "confirmed_extra", "insufficient", "pot_empty":
		default:
			return fmt.Errorf("invalid local reward verification")
		}
		if _, exists := seen[observation.BlockNumber]; exists {
			return fmt.Errorf("duplicate local reward observation")
		}
		seen[observation.BlockNumber] = struct{}{}
	}
	return nil
}

// scanLocalRewardHistory processes at most one bounded chunk. Only the local
// finalized RPC and an atomic database commit may advance the durable cursor.
func (c *controller) scanLocalRewardHistory(ctx context.Context, now time.Time, previous model.RewardOverview, finalized int64) (model.RewardOverview, bool, error) {
	if previous.LastScannedBlock <= 0 || finalized <= previous.LastScannedBlock {
		return previous, false, nil
	}
	if !c.rewardScanNext.IsZero() && now.Before(c.rewardScanNext) {
		return previous, false, nil
	}
	from := previous.LastScannedBlock + 1
	to := rewardScanEnd(previous.LastScannedBlock, finalized)
	if c.rewardScanner == nil || c.rewardWriter == nil {
		return previous, true, c.failRewardScan(now, fmt.Errorf("reward history dependency unavailable"))
	}
	scan, err := c.rewardScanner.RewardScan(ctx, from, to)
	if err != nil {
		return previous, true, c.failRewardScan(now, err)
	}
	if err := validateLocalRewardScan(scan, from, to); err != nil {
		return previous, true, c.failRewardScan(now, err)
	}
	next := previous
	next.Recovery = nil
	for index := range scan.Observations {
		observation := &scan.Observations[index]
		if observation.Verification == "insufficient" || observation.Verification == "pot_empty" {
			c.confirmRewardAnomaly(ctx, observation)
		}
		if observation.BlockNumber > next.LastAuthoredBlock {
			next.LastAuthoredBlock = observation.BlockNumber
		}
		if observation.AuthoredAt.After(next.LastRewardAt) {
			next.LastRewardAt = observation.AuthoredAt
			next.LastRewardPlanck = observation.CreditedPlanck
		}
	}
	next.LastScannedBlock = to
	if to < finalized {
		next.Gap = fmt.Sprintf("catching up after block %d", to)
	} else {
		next.Gap = ""
	}
	if err := c.rewardWriter.ApplyRewardScan(ctx, previous.LastScannedBlock, next, scan.Observations); err != nil {
		return previous, true, c.failRewardScan(now, err)
	}
	c.rewardScanFailures = 0
	c.rewardScanNext = now.Add(rewardScanInterval)
	return next, true, nil
}

func rewardScanEnd(cursor, finalized int64) int64 {
	lag := finalized - cursor
	chunk := rewardScanChunkSize
	if lag > store.RewardGapRetainedWindowBlocks {
		chunk = min(lag, rewardScanMaxChunkSize)
	}
	return min(cursor+chunk, finalized)
}

func (c *controller) failRewardScan(now time.Time, err error) error {
	c.rewardScanFailures++
	c.rewardScanNext = now.Add(rewardScanRetryBackoff(c.rewardScanFailures))
	return err
}

func (c *controller) confirmRewardAnomaly(ctx context.Context, observation *model.RewardObservation) {
	now := time.Now()
	for i, monitor := range c.rewardExternal {
		if !c.rewardExternalReady(i, now) {
			continue
		}
		scanCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		scan, err := monitor.Scan(scanCtx, observation.BlockNumber, observation.BlockNumber)
		cancel()
		if err != nil {
			c.recordRewardExternalFailure(i, now)
			continue
		}
		c.recordRewardExternalSuccess(i, now)
		if len(scan.Observations) != 1 {
			continue
		}
		x := scan.Observations[0]
		if x.BlockHash == observation.BlockHash && x.ExpectedPlanck == observation.ExpectedPlanck && x.CreditedPlanck == observation.CreditedPlanck && x.Verification == observation.Verification {
			observation.SourceCount++
			if observation.Evidence == nil {
				observation.Evidence = map[string]any{}
			}
			observation.Evidence[fmt.Sprintf("external_%d", i+1)] = "matched"
		}
	}
}

func rewardStatus(x model.RewardOverview, warningAfter, criticalAfter time.Duration) string {
	if !x.SchemaOK || x.Gap != "" || x.Quorum < 2 {
		return "degraded"
	}
	if !x.ActiveSession && x.InactiveConfirmations >= 2 {
		return "critical"
	}
	if !x.ActiveSession {
		return "verifying"
	}
	age := time.Duration(x.SecondsSinceReward) * time.Second
	if age >= criticalAfter {
		return "critical"
	}
	if age >= warningAfter {
		return "warning"
	}
	return "healthy"
}

func (c *controller) rewardSignals(ctx context.Context, x model.RewardOverview, state model.Overview) []rules.Signal {
	if !x.SchemaOK || x.Gap != "" || x.Quorum < 2 {
		return []rules.Signal{rewardSignal("reward-monitor-degraded", "warning", "報酬監視の証拠が不足しています", map[string]any{"schema_ok": x.SchemaOK, "quorum": x.Quorum, "gap": x.Gap, "sources": x.Sources})}
	}
	if !x.ActiveSession && x.InactiveConfirmations >= 2 {
		return []rules.Signal{rewardSignal("reward-active-set-missing", "critical", "報酬ウォレットがactive setに含まれていません", map[string]any{"address": x.Address, "validator_count": x.ValidatorCount, "quorum": x.Quorum})}
	}
	items, err := c.store.RewardPage(ctx, 5, 0)
	if err == nil {
		for _, item := range items {
			if (item.Verification == "insufficient" || item.Verification == "pot_empty") && item.SourceCount >= 2 {
				return []rules.Signal{rewardSignal("reward-credit-mismatch", "critical", "作成blockに対応する報酬入金が不足しています", map[string]any{"block": item.BlockNumber, "expected_planck": item.ExpectedPlanck, "credited_planck": item.CreditedPlanck, "verification": item.Verification, "sources": item.SourceCount})}
			}
		}
	}
	if state.Node.ServiceState != "active" || state.Chain.Status != "healthy" || !c.rewardChainProgressing(time.Now()) {
		if x.SecondsSinceReward >= int64(c.cfg.RewardWarnAfter.Seconds()) {
			_, _ = c.store.DB.ExecContext(ctx, `UPDATE incidents SET evidence=evidence||$1,updated_at=now() WHERE status='open' AND fingerprint NOT LIKE 'reward-%'`, store.JSON(map[string]any{"reward_acquisition_risk": map[string]any{"seconds_since_reward": x.SecondsSinceReward, "last_authored_block": x.LastAuthoredBlock, "active_set": x.ActiveSession}}))
		}
		return nil
	}
	if x.Status == "critical" {
		return []rules.Signal{rewardSignal("reward-silence", "critical", "ブロック生成報酬が30分以上確認できません", map[string]any{"last_authored_block": x.LastAuthoredBlock, "seconds": x.SecondsSinceReward, "blocks_since": x.BlocksSinceAuthored})}
	}
	if x.Status == "warning" {
		return []rules.Signal{rewardSignal("reward-silence", "warning", "ブロック生成報酬が15分以上確認できません", map[string]any{"last_authored_block": x.LastAuthoredBlock, "seconds": x.SecondsSinceReward, "blocks_since": x.BlocksSinceAuthored})}
	}
	return nil
}

func rewardSignal(fingerprint, severity, title string, evidence map[string]any) rules.Signal {
	return rules.Signal{Fingerprint: fingerprint, Severity: severity, Title: title, AutoCandidate: false, Evidence: evidence}
}

func (c *controller) rewardChainProgressing(now time.Time) bool {
	for i := range c.rewardChainProgress {
		if !c.externalOK[i] || c.rewardChainProgress[i].IsZero() || now.Sub(c.rewardChainProgress[i]) > 2*time.Minute {
			return false
		}
	}
	return true
}

func (c *controller) updateExternal(ctx context.Context) {
	c.lastExternal = time.Now()
	for i := 0; i < 2; i++ {
		if i >= len(c.cfg.ExternalRPC) {
			c.externalOK[i] = false
			continue
		}
		height, err := rpcHeight(ctx, c.cfg.ExternalRPC[i])
		if err != nil {
			slog.Warn("external rpc", "endpoint", i, "error", err)
			c.externalOK[i] = false
			continue
		}
		c.external[i] = height
		c.externalOK[i] = true
	}
}
func rpcHeight(ctx context.Context, endpoint string) (int64, error) {
	payload := []byte(`{"jsonrpc":"2.0","method":"chain_getHeader","params":[],"id":1}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 12 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if response.StatusCode/100 != 2 {
		return 0, fmt.Errorf("rpc %s", response.Status)
	}
	var result struct {
		Result struct {
			Number string `json:"number"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Result.Number == "" {
		return 0, fmt.Errorf("invalid rpc response")
	}
	value, err := strconv.ParseInt(strings.TrimPrefix(result.Result.Number, "0x"), 16, 64)
	return value, err
}

func (c *controller) handleSignals(ctx context.Context, state model.Overview, signals []rules.Signal, agentErr, metricsErr error) {
	active := map[string]bool{}
	for _, signal := range signals {
		active[signal.Fingerprint] = true
		incident, created, err := c.store.OpenIncident(ctx, signal.Fingerprint, signal.Severity, signal.Title, signal.Evidence)
		if err != nil {
			continue
		}
		if created {
			c.store.Audit(ctx, "incident.open", "controller", "success", map[string]string{"incident_id": incident.ID, "fingerprint": signal.Fingerprint})
			mailErr := c.mail.Send(ctx, "[Shiden Guardian] "+signal.Title, fmt.Sprintf("ノード: %s\n重要度: %s\n検知: %s\n\nダッシュボードで証拠と診断を確認してください。", c.cfg.NodeName, signal.Severity, time.Now().Format(time.RFC3339)))
			c.smtpHealthy = mailErr == nil
			diagnosis := c.runDiagnosis(ctx, state, incident)
			if signal.AutoCandidate {
				c.autoSeen[signal.Fingerprint] = time.Now()
			}
			c.maybeEnqueueAuto(ctx, state, signal, incident, diagnosis, agentErr, metricsErr)
		} else if severityRank(signal.Severity) > severityRank(incident.Severity) {
			_, updateErr := c.store.DB.ExecContext(ctx, `UPDATE incidents SET severity=$2,title=$3,evidence=$4,updated_at=now() WHERE id=$1 AND status='open'`, incident.ID, signal.Severity, signal.Title, store.JSON(signal.Evidence))
			if updateErr == nil {
				incident.Severity = signal.Severity
				incident.Title = signal.Title
				incident.Evidence = signal.Evidence
				c.store.Audit(ctx, "incident.escalate", "controller", "success", map[string]string{"incident_id": incident.ID, "severity": signal.Severity})
				mailErr := c.mail.Send(ctx, "[Shiden Guardian] "+signal.Title, fmt.Sprintf("ノード: %s\n重要度が %s へ変化しました。\n確認時刻: %s\n\nダッシュボードで証拠と診断を確認してください。", c.cfg.NodeName, signal.Severity, time.Now().Format(time.RFC3339)))
				c.smtpHealthy = mailErr == nil
				_ = c.runDiagnosis(ctx, state, incident)
			}
		} else if signal.AutoCandidate {
			diagnosis, _ := c.loadDiagnosis(ctx, incident.ID)
			c.maybeEnqueueAuto(ctx, state, signal, incident, diagnosis, agentErr, metricsErr)
		}
	}
	rows, err := c.store.DB.QueryContext(ctx, `SELECT id,fingerprint FROM incidents WHERE status='open'`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, fingerprint string
			if rows.Scan(&id, &fingerprint) == nil && !active[fingerprint] {
				if fingerprint == "reward-silence" && (state.Node.ServiceState != "active" || state.Chain.Status != "healthy" || !c.rewardChainProgressing(time.Now())) {
					continue
				}
				_, _ = c.store.DB.ExecContext(ctx, `UPDATE incidents SET status='resolved',resolved_at=now(),updated_at=now() WHERE id=$1`, id)
				c.store.Audit(ctx, "incident.resolve", "controller", "success", map[string]string{"incident_id": id})
				if strings.HasPrefix(fingerprint, "reward-") {
					_ = c.mail.Send(ctx, "[Shiden Guardian] 報酬監視が復旧しました", fmt.Sprintf("ノード: %s\nインシデント: %s\n状態: resolved\n確認時刻: %s", c.cfg.NodeName, fingerprint, time.Now().Format(time.RFC3339)))
				}
				delete(c.autoSeen, fingerprint)
			}
		}
	}
}

func severityRank(value string) int {
	switch value {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}
func (c *controller) runDiagnosis(ctx context.Context, state model.Overview, incident model.Incident) model.Diagnosis {
	logs, _ := c.agent.Logs(ctx, 200, "15 minutes ago", "warning", "")
	diagnosis, err := c.gemini.Diagnose(ctx, state, logs, incident)
	if err != nil {
		c.store.Audit(ctx, "diagnosis.run", "controller", "failed", map[string]string{"incident_id": incident.ID, "error": err.Error()})
		_ = c.mail.Send(ctx, "[Shiden Guardian] AI診断失敗", fmt.Sprintf("ノード: %s\nIncident: %s\nエラー: %s\n自動操作は行いません。", c.cfg.NodeName, incident.ID, err.Error()))
		return model.Diagnosis{IncidentID: incident.ID, RecommendedAction: "manual_investigation"}
	}
	_ = c.store.SetDiagnosis(ctx, incident.ID, diagnosis)
	c.store.Audit(ctx, "diagnosis.run", "controller", "success", map[string]any{"incident_id": incident.ID, "confidence": diagnosis.Confidence, "action": diagnosis.RecommendedAction})
	_ = c.mail.Send(ctx, "[Shiden Guardian] AI診断完了", fmt.Sprintf("ノード: %s\nIncident: %s\n重要度: %s\n診断: %s\n推奨: %s\nConfidence: %.2f", c.cfg.NodeName, incident.ID, diagnosis.Severity, diagnosis.Summary, diagnosis.RecommendedAction, diagnosis.Confidence))
	return diagnosis
}
func (c *controller) loadDiagnosis(ctx context.Context, incidentID string) (model.Diagnosis, error) {
	var raw []byte
	var out model.Diagnosis
	err := c.store.DB.QueryRowContext(ctx, `SELECT payload FROM diagnoses WHERE incident_id=$1 ORDER BY created_at DESC LIMIT 1`, incidentID).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func (c *controller) maybeEnqueueAuto(ctx context.Context, state model.Overview, signal rules.Signal, incident model.Incident, diagnosis model.Diagnosis, agentErr, metricsErr error) {
	first, seen := c.autoSeen[signal.Fingerprint]
	if !seen {
		c.autoSeen[signal.Fingerprint] = time.Now()
		return
	}
	safe := state.Automation.Enabled && !state.Automation.HostLocked && time.Now().After(state.Automation.EligibleAt) && time.Since(first) >= time.Minute && diagnosis.RecommendedAction == "restart_service" && diagnosis.Confidence >= .90 && c.externalOK[0] && c.externalOK[1] && agentErr == nil && metricsErr == nil && state.Host.Disk < 92 && c.smtpHealthy
	if !safe {
		return
	}
	id := store.ID("act")
	_, err := c.store.DB.ExecContext(ctx, `INSERT INTO remediation_actions(id,idempotency_key,incident_id,requested_by,reason,mode,status) VALUES($1,$2,$3,'controller',$4,'automatic','pending') ON CONFLICT(idempotency_key) DO NOTHING`, id, "auto-"+incident.ID, incident.ID, "deterministic guard and Gemini diagnosis agree")
	if err == nil {
		c.store.Audit(ctx, "restart.auto.queue", "controller", "queued", map[string]string{"action_id": id, "incident_id": incident.ID})
	}
}

func (c *controller) processJobs(ctx context.Context) {
	var id, kind string
	err := c.store.DB.QueryRowContext(ctx, `UPDATE jobs SET status='running' WHERE id=(SELECT id FROM jobs WHERE status='pending' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id,kind`).Scan(&id, &kind)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return
	}
	status := "completed"
	switch kind {
	case "diagnose_now":
		state := c.snapshot()
		incident, _, openErr := c.store.OpenIncident(ctx, "manual-diagnosis-"+id, "info", "手動AI診断", map[string]string{"job_id": id})
		if openErr != nil {
			err = openErr
		} else {
			c.runDiagnosis(ctx, state, incident)
		}
	case "test_smtp":
		err = c.mail.Send(ctx, "[Shiden Guardian] SMTP疎通テスト", fmt.Sprintf("ノード: %s\nSMTP通知は正常です。", c.cfg.NodeName))
		c.smtpHealthy = err == nil
	case "test_gemini":
		_, err = c.gemini.Diagnose(ctx, c.snapshot(), nil, model.Incident{ID: "connectivity-test", Severity: "info", Title: "Gemini疎通テスト"})
	default:
		err = fmt.Errorf("unknown job kind: %s", kind)
	}
	if err != nil {
		status = "failed"
	}
	_, _ = c.store.DB.ExecContext(ctx, `UPDATE jobs SET status=$2,finished_at=now() WHERE id=$1`, id, status)
	c.store.Audit(ctx, "job."+kind, "controller", status, map[string]any{"job_id": id, "error": errorString(err)})
}

var manualActionIDPattern = regexp.MustCompile(`^act_[A-Za-z0-9_-]{16,100}$`)

func validateManualAction(action *actionbroker.ManualAction) error {
	action.Reason = strings.TrimSpace(action.Reason)
	action.RequestedBy = strings.TrimSpace(action.RequestedBy)
	action.IncidentID = strings.TrimSpace(action.IncidentID)
	action.ActionKind = strings.TrimSpace(action.ActionKind)
	if action.ActionKind == "" {
		action.ActionKind = actionbroker.ActionRestartService
	}
	if len(action.Parameters) == 0 {
		action.Parameters = json.RawMessage(`{}`)
	}
	if !manualActionIDPattern.MatchString(action.ActionID) || len(action.IdempotencyKey) < 1 || len(action.IdempotencyKey) > 100 || action.RequestedBy == "" || len(action.Reason) < 10 || len(action.Reason) > 1000 || !json.Valid(action.Parameters) {
		return fmt.Errorf("invalid request")
	}
	switch action.ActionKind {
	case actionbroker.ActionRestartService:
		return nil
	case actionbroker.ActionAcknowledgePrunedRewardGap:
		var parameters actionbroker.RewardGapParameters
		if err := json.Unmarshal(action.Parameters, &parameters); err != nil || parameters.ExpectedCursor <= 0 {
			return fmt.Errorf("invalid reward gap parameters")
		}
		return nil
	default:
		return fmt.Errorf("unsupported action kind")
	}
}

func sameManualActionParameters(kind string, left, right json.RawMessage) bool {
	if kind == actionbroker.ActionAcknowledgePrunedRewardGap {
		var a, b actionbroker.RewardGapParameters
		return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && a == b
	}
	return kind == actionbroker.ActionRestartService
}

func (c *controller) startActionBroker() error {
	path := c.cfg.ActionBrokerSock
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0660); err != nil {
		listener.Close()
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeControllerJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/manual-actions", c.manualAction)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("action broker server", "error", err)
		}
	}()
	return nil
}

func writeControllerJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (c *controller) manualAction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, actionbroker.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeControllerJSON(w, 400, actionbroker.Result{Error: "invalid_request"})
		return
	}
	if err := c.actionVerifier.Verify(r.Header, body); err != nil {
		c.store.Audit(r.Context(), "restart.broker", "controller", "denied", map[string]string{"reason": err.Error()})
		writeControllerJSON(w, 401, actionbroker.Result{Error: "broker_authentication_failed"})
		return
	}
	var action actionbroker.ManualAction
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&action) != nil || decoder.Decode(&struct{}{}) != io.EOF || validateManualAction(&action) != nil {
		writeControllerJSON(w, 400, actionbroker.Result{Error: "invalid_request"})
		return
	}
	var id string
	err = c.store.DB.QueryRowContext(r.Context(), `INSERT INTO remediation_actions(id,idempotency_key,incident_id,requested_by,reason,action_kind,parameters,mode,status) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7,'manual','pending') ON CONFLICT(idempotency_key) DO NOTHING RETURNING id`, action.ActionID, action.IdempotencyKey, action.IncidentID, action.RequestedBy, action.Reason, action.ActionKind, store.JSON(action.Parameters)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var incidentID, requestedBy, reason, actionKind string
		var parameters json.RawMessage
		err = c.store.DB.QueryRowContext(r.Context(), `SELECT id,COALESCE(incident_id,''),requested_by,reason,action_kind,parameters FROM remediation_actions WHERE idempotency_key=$1`, action.IdempotencyKey).Scan(&id, &incidentID, &requestedBy, &reason, &actionKind, &parameters)
		if err == nil && (incidentID != action.IncidentID || requestedBy != action.RequestedBy || reason != action.Reason || actionKind != action.ActionKind || !sameManualActionParameters(actionKind, parameters, action.Parameters)) {
			writeControllerJSON(w, 409, actionbroker.Result{Error: "idempotency_conflict"})
			return
		}
	}
	if err != nil {
		writeControllerJSON(w, 500, actionbroker.Result{Error: "database"})
		return
	}
	queueAudit := "restart.manual.queue"
	if action.ActionKind == actionbroker.ActionAcknowledgePrunedRewardGap {
		queueAudit = "reward.history_gap.queue"
	}
	c.store.Audit(r.Context(), queueAudit, action.RequestedBy, "queued", map[string]string{"action_id": id, "action_kind": action.ActionKind})
	writeControllerJSON(w, 202, actionbroker.Result{ID: id, Status: "pending"})
}

func (c *controller) processPendingAction() {
	if !c.actionMu.TryLock() {
		return
	}
	defer c.actionMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	var action model.RemediationAction
	err := c.store.DB.QueryRowContext(ctx, `UPDATE remediation_actions SET status='running',started_at=now() WHERE id=(SELECT id FROM remediation_actions WHERE status='pending' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id,idempotency_key,COALESCE(incident_id,''),requested_by,reason,action_kind,parameters,mode,status,created_at`).Scan(&action.ID, &action.IdempotencyKey, &action.IncidentID, &action.RequestedBy, &action.Reason, &action.ActionKind, &action.Parameters, &action.Mode, &action.Status, &action.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return
	}
	var result any
	auditAction := "action.execute"
	subject := "[Shiden Guardian] 操作"
	disableAutomationOnFailure := false
	switch action.ActionKind {
	case actionbroker.ActionRestartService:
		baseline := c.snapshot().Chain.LocalFinalized
		result, err = c.agent.Restart(ctx, action.ID, action.Reason)
		if err == nil {
			err = c.verifyRecovery(ctx, baseline)
		}
		auditAction = "restart.execute"
		subject = "[Shiden Guardian] 再起動"
		disableAutomationOnFailure = true
	case actionbroker.ActionAcknowledgePrunedRewardGap:
		var parameters actionbroker.RewardGapParameters
		if decodeErr := json.Unmarshal(action.Parameters, &parameters); decodeErr != nil {
			err = fmt.Errorf("decode reward gap action: %w", decodeErr)
		} else {
			func() {
				c.rewardMu.Lock()
				defer c.rewardMu.Unlock()
				result, err = acknowledgePrunedRewardGap(ctx, c.agent, c.store, parameters.ExpectedCursor, store.RewardGapApproval{Actor: action.RequestedBy, Reason: action.Reason, ActionID: action.ID})
			}()
		}
		auditAction = "reward.history_gap.execute"
		subject = "[Shiden Guardian] 報酬監視履歴の再基準化"
	default:
		err = fmt.Errorf("unknown remediation action kind: %s", action.ActionKind)
	}
	status := "succeeded"
	if err != nil {
		status = "failed"
		result = map[string]any{"error": err.Error()}
		if disableAutomationOnFailure {
			_ = c.store.SetSetting(ctx, "automation_enabled", false)
		}
	}
	_, _ = c.store.DB.ExecContext(ctx, `UPDATE remediation_actions SET status=$2,result=$3,finished_at=now() WHERE id=$1`, action.ID, status, store.JSON(result))
	c.store.Audit(ctx, auditAction, "controller", status, map[string]any{"action_id": action.ID, "action_kind": action.ActionKind, "mode": action.Mode})
	_ = c.mail.Send(ctx, subject+" "+status, fmt.Sprintf("ノード: %s\nAction: %s\n種別: %s\n結果: %s\n理由: %s", c.cfg.NodeName, action.ID, action.ActionKind, status, action.Reason))
}
func (c *controller) verifyRecovery(ctx context.Context, baseline int64) error {
	activeDeadline := time.Now().Add(2 * time.Minute)
	blockDeadline := time.Now().Add(10 * time.Minute)
	active := false
	for time.Now().Before(blockDeadline) {
		snapshot, err := c.agent.Snapshot(ctx)
		if err == nil && snapshot.Node.ServiceState == "active" {
			active = true
		}
		if !active && time.Now().After(activeDeadline) {
			return fmt.Errorf("service did not become active within two minutes")
		}
		metrics, err := c.agent.Metrics(ctx)
		if err == nil && active {
			finalized := int64(metric(metrics, []string{"substrate_block_height"}, `status="finalized"`))
			if finalized > baseline {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
	return fmt.Errorf("finalized block did not advance within ten minutes")
}

func (c *controller) snapshot() model.Overview { c.mu.RLock(); defer c.mu.RUnlock(); return c.current }
func (c *controller) serveMetrics() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		state := c.snapshot()
		up := 0
		if state.Node.ServiceState == "active" {
			up = 1
		}
		activeSet := 0
		if state.Rewards.ActiveSession {
			activeSet = 1
		}
		recoveryRequired := 0
		if state.Rewards.Recovery != nil && state.Rewards.Recovery.Required {
			recoveryRequired = 1
		}
		cursorLag := max64(0, state.Rewards.FinalizedBlock-state.Rewards.LastScannedBlock)
		fmt.Fprintf(w, "# HELP guardian_node_up Whether astar.service is active.\n# TYPE guardian_node_up gauge\nguardian_node_up %d\nguardian_local_finalized %d\nguardian_external_height %d\nguardian_sync_lag %d\nguardian_peers %d\nguardian_host_cpu_percent %.3f\nguardian_host_memory_percent %.3f\nguardian_host_disk_percent %.3f\nguardian_collator_active_set %d\nguardian_collator_last_authored_block %d\nguardian_collator_blocks_since_authored %d\nguardian_collator_seconds_since_reward %d\nguardian_collator_reward_events_total %d\nguardian_collator_reward_sdn_total %.12f\nguardian_collator_reward_sdn_last %.12f\nguardian_collator_wallet_free_sdn %.12f\n# HELP guardian_collator_reward_cursor_lag Finalized blocks not yet scanned for rewards.\n# TYPE guardian_collator_reward_cursor_lag gauge\nguardian_collator_reward_cursor_lag %d\n# HELP guardian_collator_reward_recovery_required Whether an operator-approved pruning-gap recovery is required.\n# TYPE guardian_collator_reward_recovery_required gauge\nguardian_collator_reward_recovery_required %d\n", up, state.Chain.LocalFinalized, state.Chain.ExternalHeight, state.Chain.Lag, state.Chain.Peers, state.Host.CPU, state.Host.Memory, state.Host.Disk, activeSet, state.Rewards.LastAuthoredBlock, state.Rewards.BlocksSinceAuthored, state.Rewards.SecondsSinceReward, state.Rewards.RewardTotalCount, planckSDN(state.Rewards.RewardTotalPlanck), planckSDN(state.Rewards.LastRewardPlanck), planckSDN(state.Rewards.WalletFreePlanck), cursorLag, recoveryRequired)
		for source, status := range state.Rewards.Sources {
			ok := 0
			if status == "ok" {
				ok = 1
			}
			fmt.Fprintf(w, "guardian_reward_rpc_up{source=%q} %d\n", source, ok)
		}
	})
	server := http.Server{Addr: ":9091", Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		slog.Error("metrics server", "error", err)
	}
}
func planckSDN(value string) float64 {
	integer, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return 0
	}
	ratio := new(big.Rat).SetFrac(integer, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	out, _ := ratio.Float64()
	return out
}

func metric(raw string, names []string, label string) float64 {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || label != "" && !strings.Contains(line, label) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		metricName := strings.Split(fields[0], "{")[0]
		matched := false
		for _, name := range names {
			if metricName == name {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err == nil && !math.IsNaN(value) {
			return value
		}
	}
	return 0
}
func max64(values ...int64) int64 {
	var out int64
	for _, v := range values {
		if v > out {
			out = v
		}
	}
	return out
}
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

package rules

import (
	"fmt"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

type Signal struct {
	Fingerprint   string
	Severity      string
	Title         string
	AutoCandidate bool
	Evidence      map[string]any
}
type Evaluator struct {
	lastFinalized    int64
	lastProgress     time.Time
	stallExternalA   int64
	stallExternalB   int64
	inactiveSince    time.Time
	lowPeerSince     time.Time
	zeroPeerSince    time.Time
	highMemorySince  time.Time
	warnLagSince     time.Time
	criticalLagSince time.Time
}

func (e *Evaluator) Evaluate(now time.Time, state model.Overview) []Signal {
	var out []Signal
	if state.Node.ServiceState != "active" {
		if e.inactiveSince.IsZero() {
			e.inactiveSince = now
		}
		if now.Sub(e.inactiveSince) >= time.Minute {
			out = append(out, Signal{"service-inactive", "critical", "astar.service が60秒以上停止", true, map[string]any{"state": state.Node.ServiceState, "duration_seconds": int(now.Sub(e.inactiveSince).Seconds())}})
		}
	} else {
		e.inactiveSince = time.Time{}
	}

	if state.Chain.LocalFinalized > 0 && state.Chain.LocalFinalized != e.lastFinalized {
		e.lastFinalized = state.Chain.LocalFinalized
		e.lastProgress = now
		e.stallExternalA = state.Chain.ExternalA
		e.stallExternalB = state.Chain.ExternalB
	}
	if e.lastProgress.IsZero() && state.Chain.LocalFinalized > 0 {
		e.lastProgress = now
		e.lastFinalized = state.Chain.LocalFinalized
		e.stallExternalA = state.Chain.ExternalA
		e.stallExternalB = state.Chain.ExternalB
	}
	if !e.lastProgress.IsZero() && now.Sub(e.lastProgress) >= 5*time.Minute && state.Chain.ExternalA-e.stallExternalA >= 20 && state.Chain.ExternalB-e.stallExternalB >= 20 {
		out = append(out, Signal{"block-stalled", "critical", "外部チェーン進行中にローカルblockが5分停止", true, map[string]any{"local_finalized": state.Chain.LocalFinalized, "external_a_delta": state.Chain.ExternalA - e.stallExternalA, "external_b_delta": state.Chain.ExternalB - e.stallExternalB}})
	}

	if state.Chain.Lag > 120 {
		if e.criticalLagSince.IsZero() {
			e.criticalLagSince = now
		}
		if now.Sub(e.criticalLagSince) >= 10*time.Minute {
			out = append(out, Signal{"sync-lag-critical", "critical", fmt.Sprintf("同期差が%d blocksまで拡大", state.Chain.Lag), false, map[string]any{"lag": state.Chain.Lag}})
		}
	} else {
		e.criticalLagSince = time.Time{}
	}
	if state.Chain.Lag > 30 {
		if e.warnLagSince.IsZero() {
			e.warnLagSince = now
		}
		if now.Sub(e.warnLagSince) >= 5*time.Minute {
			out = append(out, Signal{"sync-lag-warning", "warning", fmt.Sprintf("同期差が%d blocks", state.Chain.Lag), false, map[string]any{"lag": state.Chain.Lag}})
		}
	} else {
		e.warnLagSince = time.Time{}
	}
	if state.Chain.Peers == 0 && state.Node.ServiceState == "active" {
		if e.zeroPeerSince.IsZero() {
			e.zeroPeerSince = now
		}
		if now.Sub(e.zeroPeerSince) >= 3*time.Minute {
			out = append(out, Signal{"peers-zero", "critical", "接続peerが3分間0", false, map[string]any{"peers": 0}})
		}
	} else {
		e.zeroPeerSince = time.Time{}
	}
	if state.Chain.Peers > 0 && state.Chain.Peers < 3 {
		if e.lowPeerSince.IsZero() {
			e.lowPeerSince = now
		}
		if now.Sub(e.lowPeerSince) >= 5*time.Minute {
			out = append(out, Signal{"peers-low", "warning", "接続peerが5分間3未満", false, map[string]any{"peers": state.Chain.Peers}})
		}
	} else {
		e.lowPeerSince = time.Time{}
	}
	if state.Host.Disk >= 92 {
		out = append(out, Signal{"disk-critical", "critical", "ディスク空き容量が8%未満", false, map[string]any{"used_percent": state.Host.Disk}})
	} else if state.Host.Disk >= 85 {
		out = append(out, Signal{"disk-warning", "warning", "ディスク空き容量が15%未満", false, map[string]any{"used_percent": state.Host.Disk}})
	}
	if state.Host.Memory >= 90 {
		if e.highMemorySince.IsZero() {
			e.highMemorySince = now
		}
		if now.Sub(e.highMemorySince) >= 10*time.Minute {
			out = append(out, Signal{"memory-high", "warning", "メモリ使用率が10分間90%以上", false, map[string]any{"used_percent": state.Host.Memory}})
		}
	} else {
		e.highMemorySince = time.Time{}
	}
	return out
}

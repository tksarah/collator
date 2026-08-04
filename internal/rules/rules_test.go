package rules

import (
	"github.com/shiden-guardian/shiden-guardian/internal/model"
	"testing"
	"time"
)

func base() model.Overview {
	return model.Overview{Node: model.NodeState{ServiceState: "active"}, Chain: model.ChainState{LocalFinalized: 100, ExternalA: 100, ExternalB: 100, Peers: 20}, Host: model.HostState{Memory: 50, Disk: 50}}
}
func TestInactiveRequiresSixtySeconds(t *testing.T) {
	e := Evaluator{}
	now := time.Now()
	s := base()
	s.Node.ServiceState = "failed"
	if got := e.Evaluate(now, s); len(got) != 0 {
		t.Fatal("early signal")
	}
	got := e.Evaluate(now.Add(61*time.Second), s)
	if len(got) != 1 || !got[0].AutoCandidate {
		t.Fatalf("unexpected: %#v", got)
	}
}
func TestGlobalStallDoesNotRestart(t *testing.T) {
	e := Evaluator{}
	now := time.Now()
	s := base()
	e.Evaluate(now, s)
	got := e.Evaluate(now.Add(6*time.Minute), s)
	if len(got) != 0 {
		t.Fatalf("global stall must not signal: %#v", got)
	}
}
func TestLocalStallNeedsTwoExternalReferences(t *testing.T) {
	e := Evaluator{}
	now := time.Now()
	s := base()
	e.Evaluate(now, s)
	s.Chain.ExternalA = 130
	s.Chain.ExternalB = 105
	if got := e.Evaluate(now.Add(6*time.Minute), s); len(got) != 0 {
		t.Fatal("single reference must not signal")
	}
	s.Chain.ExternalB = 130
	got := e.Evaluate(now.Add(6*time.Minute+time.Second), s)
	if len(got) != 1 || got[0].Fingerprint != "block-stalled" {
		t.Fatalf("unexpected: %#v", got)
	}
}
func TestDiskNeverAutoRestarts(t *testing.T) {
	e := Evaluator{}
	s := base()
	s.Host.Disk = 94
	got := e.Evaluate(time.Now(), s)
	if len(got) != 1 || got[0].AutoCandidate {
		t.Fatalf("unexpected: %#v", got)
	}
}

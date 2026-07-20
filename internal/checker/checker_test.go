package checker

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/alechill/quorum/internal/history"
)

func load(t *testing.T, name string) []history.Op {
	t.Helper()
	ops, err := history.ReadFile(filepath.Join("..", "..", "harness", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return ops
}

// The checker must reject the committed known-bad history: a read that
// returns a stale value strictly after an acknowledged overwrite.
func TestCheckerRejectsKnownBad(t *testing.T) {
	res := Check(load(t, "known-bad-history.jsonl"), time.Minute)
	if res.Linearizable {
		t.Fatal("checker accepted the known non-linearizable history")
	}
}

func TestCheckerAcceptsKnownGood(t *testing.T) {
	res := Check(load(t, "known-good-history.jsonl"), time.Minute)
	if !res.Linearizable {
		t.Fatal("checker rejected a valid linearizable history")
	}
}

// A CAS that succeeds when the register cannot have held the expected value
// must be rejected.
func TestCheckerRejectsImpossibleCAS(t *testing.T) {
	a, b := "a", "never"
	ops := []history.Op{
		{Client: 1, Op: "put", Key: "k", Value: a, StartNs: 10, EndNs: 20, Outcome: "ok", Ok: true},
		{Client: 2, Op: "cas", Key: "k", Expect: &b, Value: "x", StartNs: 30, EndNs: 40, Outcome: "ok", Ok: true},
	}
	if Check(ops, time.Minute).Linearizable {
		t.Fatal("checker accepted a CAS success with an impossible expected value")
	}
}

// Two CAS successes from the same expected value (double winner) must be
// rejected — exactly one winner is the invariant.
func TestCheckerRejectsDoubleCASWinner(t *testing.T) {
	base := "base"
	ops := []history.Op{
		{Client: 1, Op: "put", Key: "k", Value: base, StartNs: 0, EndNs: 5, Outcome: "ok", Ok: true},
		{Client: 2, Op: "cas", Key: "k", Expect: &base, Value: "w1", StartNs: 10, EndNs: 20, Outcome: "ok", Ok: true},
		{Client: 3, Op: "cas", Key: "k", Expect: &base, Value: "w2", StartNs: 12, EndNs: 22, Outcome: "ok", Ok: true},
	}
	if Check(ops, time.Minute).Linearizable {
		t.Fatal("checker accepted two CAS winners for the same expected value")
	}
}

// An unknown-outcome write that never became visible must NOT fail the check
// (it may simply never have committed).
func TestUnknownWriteMayVanish(t *testing.T) {
	ops := []history.Op{
		{Client: 1, Op: "put", Key: "k", Value: "a", StartNs: 0, EndNs: 5, Outcome: "ok", Ok: true},
		{Client: 2, Op: "put", Key: "k", Value: "ghost", StartNs: 10, EndNs: 15, Outcome: "unknown"},
		{Client: 1, Op: "get", Key: "k", StartNs: 20, EndNs: 25, Outcome: "ok", Ok: true, Present: true, RetVal: "a"},
	}
	if !Check(ops, time.Minute).Linearizable {
		t.Fatal("checker rejected a history where an unacknowledged write was simply lost")
	}
}

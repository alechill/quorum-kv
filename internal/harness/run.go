package harness

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/anishathalye/porcupine"

	"github.com/alechill/quorum/internal/checker"
	"github.com/alechill/quorum/internal/history"
)

// CheckResult is one named per-run check.
type CheckResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

// Report is the machine-readable per-run result.
type Report struct {
	StartedAt    time.Time      `json:"started_at"`
	FinishedAt   time.Time      `json:"finished_at"`
	Seed         int64          `json:"seed"`
	Checks       []CheckResult  `json:"checks"`
	Faults       []FaultEvent   `json:"faults"`
	OpsTotal     int            `json:"ops_total"`
	OpsOK        int            `json:"ops_ok"`
	OpsFailed    int            `json:"ops_failed"`
	OpsUnknown   int            `json:"ops_unknown"`
	HistoryFile  string         `json:"history_file"`
	AllPassed    bool           `json:"all_passed"`
}

// Options configures a harness run.
type Options struct {
	Nodes       []Node
	Duration    time.Duration // chaos phase length
	NumClients  int
	NumKeys     int
	Seed        int64
	OutDir      string
	FixturesDir string
	SkipFaults  bool // traffic only (used for quick smoke runs)
	Logf        func(format string, args ...interface{})
}

type runner struct {
	Options
	rec    *Recorder
	faults *Faults
	report *Report
}

// Run executes the full verification suite and returns the report.
func Run(opts Options) (*Report, error) {
	if opts.Logf == nil {
		opts.Logf = func(format string, args ...interface{}) {
			fmt.Printf(format+"\n", args...)
		}
	}
	r := &runner{
		Options: opts,
		rec:     NewRecorder(),
		faults:  &Faults{Nodes: opts.Nodes, Logf: opts.Logf},
		report:  &Report{StartedAt: time.Now(), Seed: opts.Seed},
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, err
	}

	r.phaseCheckerSelfTest()
	if r.failed() {
		// A broken checker invalidates everything else; stop here.
		return r.finish()
	}
	r.phaseReset()
	r.phaseSmoke()
	r.phaseCASContention()
	if !r.SkipFaults {
		r.phaseLeaderKillDurability()
		r.phaseMinorityRefusesWrites()
		r.phaseChaos()
		r.phaseConvergence()
	}
	r.phaseLinearizability()
	return r.finish()
}

func (r *runner) check(name string, pass bool, detail string) {
	r.Logf("check %-28s %v  %s", name, passStr(pass), detail)
	r.report.Checks = append(r.report.Checks, CheckResult{Name: name, Pass: pass, Detail: detail})
}

func passStr(p bool) string {
	if p {
		return "PASS"
	}
	return "FAIL"
}

func (r *runner) failed() bool {
	for _, c := range r.report.Checks {
		if !c.Pass {
			return true
		}
	}
	return false
}

func (r *runner) finish() (*Report, error) {
	r.report.FinishedAt = time.Now()
	ops := r.rec.Ops()
	r.report.OpsTotal = len(ops)
	for _, op := range ops {
		switch op.Outcome {
		case history.OutcomeOK:
			r.report.OpsOK++
		case history.OutcomeFailed:
			r.report.OpsFailed++
		case history.OutcomeUnknown:
			r.report.OpsUnknown++
		}
	}
	r.report.Faults = r.faults.Events
	histFile := filepath.Join(r.OutDir, "history.jsonl")
	if err := history.WriteFile(histFile, ops); err != nil {
		return nil, err
	}
	r.report.HistoryFile = histFile
	r.report.AllPassed = !r.failed()

	repFile := filepath.Join(r.OutDir, "report.json")
	b, _ := json.MarshalIndent(r.report, "", "  ")
	if err := os.WriteFile(repFile, b, 0o644); err != nil {
		return nil, err
	}

	r.Logf("")
	r.Logf("=== harness result: %s (%d checks, ops: %d ok / %d failed / %d unknown) ===",
		passStr(r.report.AllPassed), len(r.report.Checks), r.report.OpsOK, r.report.OpsFailed, r.report.OpsUnknown)
	return r.report, nil
}

// --- Phase: checker self-test -------------------------------------------

// The checker is itself checked: it must reject the committed known-bad
// history and accept the known-good one.
func (r *runner) phaseCheckerSelfTest() {
	bad, err := history.ReadFile(filepath.Join(r.FixturesDir, "known-bad-history.jsonl"))
	if err != nil {
		r.check("checker-rejects-known-bad", false, "cannot read fixture: "+err.Error())
		return
	}
	res := checker.Check(bad, time.Minute)
	r.check("checker-rejects-known-bad", !res.Linearizable,
		fmt.Sprintf("known-bad history (stale read after acked overwrite) rejected=%v", !res.Linearizable))

	good, err := history.ReadFile(filepath.Join(r.FixturesDir, "known-good-history.jsonl"))
	if err != nil {
		r.check("checker-accepts-known-good", false, "cannot read fixture: "+err.Error())
		return
	}
	res = checker.Check(good, time.Minute)
	r.check("checker-accepts-known-good", res.Linearizable,
		fmt.Sprintf("known-good history accepted=%v", res.Linearizable))
}

// --- Phase: reset -----------------------------------------------------------

// Node volumes persist across harness runs, but the checker's model starts
// from an empty register. Recorded deletes of every key this run will touch
// bring model and reality into agreement: any later read linearizes after
// its key's delete, so pre-run state can never leak into the history.
func (r *runner) phaseReset() {
	keys := []string{"smoke", "majority-live", "minority-probe"}
	for i := 0; i < r.NumKeys; i++ {
		keys = append(keys, fmt.Sprintf("k%d", i))
	}
	for i := 0; i < 3; i++ {
		keys = append(keys, fmt.Sprintf("durable-%d", i), fmt.Sprintf("cas-arena-%d", i))
	}
	c := NewClient(999, r.Nodes, r.rec, r.Seed+999)
	failed := 0
	for _, k := range keys {
		op := c.Invoke(history.Op{Client: 999, Op: "delete", Key: k}, 20*time.Second, false, nil)
		if op.Outcome != history.OutcomeOK {
			failed++
		}
	}
	r.check("reset-initial-state", failed == 0,
		fmt.Sprintf("deleted %d workload keys (%d failed)", len(keys), failed))
}

// --- Phase: smoke ---------------------------------------------------------

// Basic put/get through every node's endpoint (non-leaders proxy).
func (r *runner) phaseSmoke() {
	c := NewClient(0, r.Nodes, r.rec, r.Seed)
	okAll := true
	detail := ""
	for i, n := range r.Nodes {
		val := fmt.Sprintf("smoke-%d", i)
		w := c.Invoke(history.Op{Client: 0, Op: "put", Key: "smoke", Value: val}, 20*time.Second, false, &n)
		if w.Outcome != history.OutcomeOK {
			okAll = false
			detail += fmt.Sprintf("put via %s: %s; ", n.ID, w.Outcome)
			continue
		}
		g := c.Invoke(history.Op{Client: 0, Op: "get", Key: "smoke"}, 20*time.Second, false, &n)
		if g.Outcome != history.OutcomeOK || !g.Present || g.RetVal != val {
			okAll = false
			detail += fmt.Sprintf("get via %s: outcome=%s val=%q; ", n.ID, g.Outcome, g.RetVal)
		}
	}
	if detail == "" {
		detail = "put+get through each node's endpoint"
	}
	r.check("smoke-all-nodes", okAll, detail)
}

// --- Phase: CAS contention -------------------------------------------------

// Concurrent CAS with the same expected value: exactly one winner per round.
func (r *runner) phaseCASContention() {
	rounds := 3
	contenders := 6
	allPass := true
	detail := ""
	for round := 0; round < rounds; round++ {
		key := fmt.Sprintf("cas-arena-%d", round)
		base := fmt.Sprintf("base-%d", round)
		setup := NewClient(100, r.Nodes, r.rec, r.Seed+int64(round))
		w := setup.Invoke(history.Op{Client: 100, Op: "put", Key: key, Value: base}, 20*time.Second, false, nil)
		if w.Outcome != history.OutcomeOK {
			allPass = false
			detail += fmt.Sprintf("round %d: setup put failed; ", round)
			continue
		}
		var wg sync.WaitGroup
		results := make([]history.Op, contenders)
		for i := 0; i < contenders; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				cl := NewClient(101+i, r.Nodes, r.rec, r.Seed+int64(round*100+i))
				exp := base
				results[i] = cl.Invoke(history.Op{
					Client: 101 + i, Op: "cas", Key: key,
					Expect: &exp, Value: fmt.Sprintf("winner-%d-%d", round, i),
				}, 20*time.Second, false, nil)
			}(i)
		}
		wg.Wait()
		wins, unknowns := 0, 0
		for _, res := range results {
			switch {
			case res.Outcome == history.OutcomeOK && res.Ok:
				wins++
			case res.Outcome == history.OutcomeUnknown:
				unknowns++
			}
		}
		// On a quiet cluster all outcomes should be known and exactly one
		// CAS must win. If anything was ambiguous, the hard bound is <=1
		// known winner (the checker still verifies the full semantics).
		roundOK := (unknowns == 0 && wins == 1) || (unknowns > 0 && wins <= 1)
		if !roundOK {
			allPass = false
		}
		detail += fmt.Sprintf("round %d: %d winners, %d unknown; ", round, wins, unknowns)
	}
	r.check("cas-single-winner", allPass, detail)
}

// --- Phase: leader kill durability ------------------------------------------

// Write, ack, SIGKILL the leader immediately, then the value must still be
// readable (nothing else writes these keys).
func (r *runner) phaseLeaderKillDurability() {
	iterations := 3
	allPass := true
	detail := ""
	for i := 0; i < iterations; i++ {
		key := fmt.Sprintf("durable-%d", i)
		val := fmt.Sprintf("precious-%d", i)
		c := NewClient(200+i, r.Nodes, r.rec, r.Seed+int64(i))
		w := c.Invoke(history.Op{Client: 200 + i, Op: "put", Key: key, Value: val}, 20*time.Second, false, nil)
		if w.Outcome != history.OutcomeOK {
			allPass = false
			detail += fmt.Sprintf("iter %d: put not acked (%s); ", i, w.Outcome)
			continue
		}
		leader := r.faults.LeaderID()
		if n := r.faults.NodeByID(leader); n != nil {
			_ = r.faults.Kill(*n)
			defer func(n Node) { _, _ = run("docker", "start", n.Container) }(*n)
		}
		// Read back; retry until the cluster elects a new leader.
		var got history.Op
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			got = c.Invoke(history.Op{Client: 200 + i, Op: "get", Key: key}, 5*time.Second, false, nil)
			if got.Outcome == history.OutcomeOK {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if got.Outcome != history.OutcomeOK || !got.Present || got.RetVal != val {
			allPass = false
			detail += fmt.Sprintf("iter %d: acked write lost (outcome=%s present=%v val=%q); ",
				i, got.Outcome, got.Present, got.RetVal)
		} else {
			detail += fmt.Sprintf("iter %d: survived leader kill; ", i)
		}
		// Bring the killed node back before the next iteration.
		r.faults.HealAll()
		time.Sleep(3 * time.Second)
	}
	r.check("acked-write-survives-leader-kill", allPass, detail)
}

// --- Phase: minority refuses writes -----------------------------------------

// Partition the leader away from the majority; direct (proxy-disabled)
// writes to it must never be acknowledged, and the majority side must keep
// serving.
func (r *runner) phaseMinorityRefusesWrites() {
	leaderID := r.faults.LeaderID()
	leader := r.faults.NodeByID(leaderID)
	if leader == nil {
		r.check("minority-refuses-writes", false, "no leader found to partition")
		return
	}
	_ = r.faults.Isolate(*leader)
	time.Sleep(2 * time.Second) // let the lease lapse

	// Probe the isolated ex-leader directly for ~4s.
	c := NewClient(300, r.Nodes, r.rec, r.Seed+300)
	acked := 0
	for probe := 0; probe < 4; probe++ {
		op := c.Invoke(history.Op{
			Client: 300, Op: "put", Key: "minority-probe",
			Value: fmt.Sprintf("must-not-ack-%d", probe),
		}, 4*time.Second, true, leader)
		if op.Outcome == history.OutcomeOK {
			acked++
		}
	}

	// Meanwhile the majority side must elect and serve.
	var majority []Node
	for _, n := range r.Nodes {
		if n.ID != leader.ID {
			majority = append(majority, n)
		}
	}
	// The majority needs an election timeout (and proxy fail-fast) to elect a
	// replacement; keep writing until one is acknowledged or 30s elapse.
	mc := NewClient(301, majority, r.rec, r.Seed+301)
	writeAcked, readOK := false, false
	mdeadline := time.Now().Add(30 * time.Second)
	for i := 0; time.Now().Before(mdeadline); i++ {
		mw := mc.Invoke(history.Op{
			Client: 301, Op: "put", Key: "majority-live",
			Value: fmt.Sprintf("served-%d", i),
		}, 6*time.Second, false, nil)
		if mw.Outcome == history.OutcomeOK {
			writeAcked = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if writeAcked {
		for time.Now().Before(mdeadline.Add(10 * time.Second)) {
			mg := mc.Invoke(history.Op{Client: 301, Op: "get", Key: "majority-live"}, 6*time.Second, false, nil)
			if mg.Outcome == history.OutcomeOK && strings.HasPrefix(mg.RetVal, "served-") {
				readOK = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	majorityOK := writeAcked && readOK

	r.faults.HealAll()
	time.Sleep(3 * time.Second)

	r.check("minority-refuses-writes", acked == 0,
		fmt.Sprintf("isolated leader %s acked %d/4 direct writes (must be 0)", leader.ID, acked))
	r.check("majority-keeps-serving", majorityOK,
		fmt.Sprintf("majority side during leader partition: write acked=%v, read ok=%v", writeAcked, readOK))
}

// --- Phase: chaos -----------------------------------------------------------

// Randomized concurrent traffic while the injector kills nodes (leader
// included) and imposes/heals partitions.
func (r *runner) phaseChaos() {
	r.Logf("chaos: %v of randomized traffic with fault injection", r.Duration)
	keys := make([]string, r.NumKeys)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < r.NumClients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cl := NewClient(id, r.Nodes, r.rec, r.Seed+int64(id))
			for {
				select {
				case <-stop:
					return
				default:
				}
				cl.Invoke(cl.RandomOp(keys), 8*time.Second, false, nil)
				time.Sleep(time.Duration(cl.Rng.Intn(40)) * time.Millisecond)
			}
		}(1 + i)
	}

	// Fault schedule: one fault at a time, always healed before the next.
	rng := rand.New(rand.NewSource(r.Seed ^ 0x5eed))
	end := time.Now().Add(r.Duration)
	for time.Now().Before(end) {
		time.Sleep(time.Duration(2000+rng.Intn(3000)) * time.Millisecond)
		var target *Node
		mode := rng.Intn(4)
		if mode <= 1 { // 50%: target the current leader
			target = r.faults.NodeByID(r.faults.LeaderID())
		}
		if target == nil {
			target = &r.Nodes[rng.Intn(len(r.Nodes))]
		}
		if mode%2 == 0 {
			_ = r.faults.Kill(*target)
			time.Sleep(time.Duration(3000+rng.Intn(5000)) * time.Millisecond)
			_ = r.faults.Start(*target)
		} else {
			_ = r.faults.Isolate(*target)
			time.Sleep(time.Duration(5000+rng.Intn(8000)) * time.Millisecond)
			r.faults.HealAll()
		}
		time.Sleep(time.Duration(1000+rng.Intn(2000)) * time.Millisecond)
	}

	close(stop)
	wg.Wait()
	r.faults.HealAll()
	r.check("chaos-completed", true,
		fmt.Sprintf("%d faults injected over %v", len(r.faults.Events), r.Duration))
}

// --- Phase: convergence -------------------------------------------------------

// After healing, all three nodes must converge to identical committed state
// without operator action.
func (r *runner) phaseConvergence() {
	r.faults.HealAll()
	type state struct {
		Node        string            `json:"node"`
		LastApplied string            `json:"last_applied"`
		Data        map[string]string `json:"data"`
	}
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	var detail string
	for time.Now().Before(deadline) {
		var states []state
		for _, n := range r.Nodes {
			resp, err := client.Get(n.URL + "/internal/state")
			if err != nil {
				break
			}
			var s state
			if json.NewDecoder(resp.Body).Decode(&s) != nil {
				resp.Body.Close()
				break
			}
			resp.Body.Close()
			states = append(states, s)
		}
		if len(states) == 3 {
			identical := reflect.DeepEqual(states[0].Data, states[1].Data) &&
				reflect.DeepEqual(states[1].Data, states[2].Data) &&
				states[0].LastApplied == states[1].LastApplied &&
				states[1].LastApplied == states[2].LastApplied
			if identical {
				r.check("replicas-converge", true,
					fmt.Sprintf("all 3 nodes identical: %d keys at applied index %s",
						len(states[0].Data), states[0].LastApplied))
				return
			}
			detail = fmt.Sprintf("applied indexes: %s/%s/%s",
				states[0].LastApplied, states[1].LastApplied, states[2].LastApplied)
		} else {
			detail = fmt.Sprintf("only %d/3 nodes reachable", len(states))
		}
		time.Sleep(2 * time.Second)
	}
	r.check("replicas-converge", false, "no convergence within 90s: "+detail)
}

// --- Phase: linearizability -----------------------------------------------

// The full recorded history — every phase above — must be linearizable.
func (r *runner) phaseLinearizability() {
	ops := r.rec.Ops()
	res := checker.Check(ops, 5*time.Minute)
	detail := fmt.Sprintf("%d recorded ops (%d checkable after exclusions)", len(ops), res.Ops)
	if !res.Linearizable {
		vizPath := filepath.Join(r.OutDir, "linearizability-violation.html")
		if f, err := os.Create(vizPath); err == nil {
			_ = porcupine.Visualize(checker.Model, res.Info, f)
			f.Close()
			detail += "; violation visualization: " + vizPath
		}
	}
	r.check("history-linearizable", res.Linearizable, detail)
}

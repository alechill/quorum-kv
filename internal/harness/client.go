package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"

	"github.com/alechill/quorum/internal/history"
)

// Recorder collects the client-side history. Timestamps are nanoseconds on a
// single monotonic clock (time.Since(base)), recorded at invocation and
// response by the issuing client itself.
type Recorder struct {
	mu   sync.Mutex
	base time.Time
	ops  []history.Op
}

func NewRecorder() *Recorder {
	return &Recorder{base: time.Now()}
}

func (r *Recorder) now() int64 { return int64(time.Since(r.base)) }

func (r *Recorder) add(op history.Op) {
	r.mu.Lock()
	r.ops = append(r.ops, op)
	r.mu.Unlock()
}

func (r *Recorder) Ops() []history.Op {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]history.Op, len(r.ops))
	copy(out, r.ops)
	return out
}

// Client issues operations against the cluster and records every outcome.
type Client struct {
	ID       int
	Nodes    []Node
	Rec      *Recorder
	HTTP     *http.Client
	Rng      *rand.Rand
	seq      int
	lastSeen map[string]*string // last value this client observed per key (nil = absent)
}

func NewClient(id int, nodes []Node, rec *Recorder, seed int64) *Client {
	return &Client{
		ID:    id,
		Nodes: nodes,
		Rec:   rec,
		HTTP: &http.Client{
			Timeout: 4 * time.Second,
			// One connection pool per client; no retries at transport level.
		},
		Rng:      rand.New(rand.NewSource(seed)),
		lastSeen: map[string]*string{},
	}
}

// attemptResult classifies one HTTP attempt.
type attemptResult struct {
	class   string // "ok" | "fail" | "unknown"
	ok      bool
	present bool
	val     string
}

var errDefinite = errors.New("definitely not applied")

// doAttempt sends the operation to one node and classifies the outcome.
// Classification is deliberately conservative: only errors that provably
// mean "the request never reached a server that could apply it" count as
// definite failures; everything else ambiguous is unknown.
func (c *Client) doAttempt(ctx context.Context, node Node, op history.Op, noProxy bool) attemptResult {
	var req *http.Request
	var err error
	base := node.URL + "/kv/" + op.Key
	switch op.Op {
	case "put":
		b, _ := json.Marshal(map[string]string{"value": op.Value})
		req, err = http.NewRequestWithContext(ctx, http.MethodPut, base, bytes.NewReader(b))
	case "get":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	case "delete":
		req, err = http.NewRequestWithContext(ctx, http.MethodDelete, base, nil)
	case "cas":
		b, _ := json.Marshal(map[string]interface{}{"expect": op.Expect, "value": op.Value})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/cas", bytes.NewReader(b))
	default:
		panic("bad op " + op.Op)
	}
	if err != nil {
		panic(err)
	}
	if noProxy {
		req.Header.Set("X-Quorum-No-Proxy", "1")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if isDefinitelyNotSent(err) {
			return attemptResult{class: "fail"}
		}
		return attemptResult{class: "unknown"}
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if rerr != nil {
		return attemptResult{class: "unknown"}
	}

	if resp.StatusCode == http.StatusOK {
		var res struct {
			Ok      bool   `json:"ok"`
			Present bool   `json:"present"`
			Value   string `json:"value"`
		}
		if json.Unmarshal(body, &res) != nil {
			return attemptResult{class: "unknown"}
		}
		return attemptResult{class: "ok", ok: res.Ok, present: res.Present, val: res.Value}
	}

	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	switch e.Error {
	case "no_leader", "not_leader", "bad_request":
		// Server-issued definite rejection: not applied.
		return attemptResult{class: "fail"}
	default:
		// apply_failed, proxy_error, or anything unparseable: ambiguous.
		return attemptResult{class: "unknown"}
	}
}

// isDefinitelyNotSent reports whether the error guarantees no server
// processed the request (so a retry cannot double-apply).
func isDefinitelyNotSent(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	// Dial-phase timeouts never sent the request. (Timeouts after dial are
	// ambiguous and intentionally NOT matched here.)
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	return false
}

// Invoke runs one logical operation: it tries nodes (rotating on definite
// failures only) until success, ambiguity, or the overall deadline. Each
// attempt is recorded with its own invocation window; earlier definite-fail
// attempts had no effect so only the final attempt is history-relevant.
func (c *Client) Invoke(op history.Op, deadline time.Duration, noProxy bool, pin *Node) history.Op {
	overall := time.Now().Add(deadline)
	start := c.Rng.Intn(len(c.Nodes))
	attempt := 0
	for {
		var node Node
		if pin != nil {
			node = *pin
		} else {
			node = c.Nodes[(start+attempt)%len(c.Nodes)]
		}
		attempt++

		op.Node = node.ID
		op.StartNs = c.Rec.now()
		ctx, cancel := context.WithDeadline(context.Background(), overall.Add(4*time.Second))
		res := c.doAttempt(ctx, node, op, noProxy)
		cancel()
		op.EndNs = c.Rec.now()

		switch res.class {
		case "ok":
			op.Outcome = history.OutcomeOK
			op.Ok = res.ok
			op.Present = res.present
			op.RetVal = res.val
			c.noteSeen(op)
			c.Rec.add(op)
			return op
		case "unknown":
			op.Outcome = history.OutcomeUnknown
			c.Rec.add(op)
			return op
		case "fail":
			if time.Now().After(overall) || (pin != nil) {
				op.Outcome = history.OutcomeFailed
				c.Rec.add(op)
				return op
			}
			time.Sleep(150 * time.Millisecond) // brief backoff, try next node
		}
	}
}

// noteSeen tracks the latest value this client knows for a key, to give CAS
// a realistic chance of succeeding.
func (c *Client) noteSeen(op history.Op) {
	switch op.Op {
	case "put":
		v := op.Value
		c.lastSeen[op.Key] = &v
	case "cas":
		if op.Ok {
			v := op.Value
			c.lastSeen[op.Key] = &v
		} else if op.Present {
			v := op.RetVal
			c.lastSeen[op.Key] = &v
		} else {
			c.lastSeen[op.Key] = nil
		}
	case "delete":
		c.lastSeen[op.Key] = nil
	case "get":
		if op.Present {
			v := op.RetVal
			c.lastSeen[op.Key] = &v
		} else {
			c.lastSeen[op.Key] = nil
		}
	}
}

// RandomOp generates the next randomized operation for the chaos workload.
func (c *Client) RandomOp(keys []string) history.Op {
	c.seq++
	key := keys[c.Rng.Intn(len(keys))]
	// Random component makes values unique across runs too, so residue from
	// an earlier run can never masquerade as this run's write.
	val := fmt.Sprintf("c%d-%d-%06x", c.ID, c.seq, c.Rng.Int31n(1<<24))
	switch r := c.Rng.Float64(); {
	case r < 0.35:
		return history.Op{Client: c.ID, Op: "put", Key: key, Value: val}
	case r < 0.70:
		return history.Op{Client: c.ID, Op: "get", Key: key}
	case r < 0.90:
		expect := c.lastSeen[key]
		if c.Rng.Float64() < 0.2 {
			expect = nil // occasionally cas-if-absent
		}
		return history.Op{Client: c.ID, Op: "cas", Key: key, Value: val, Expect: expect}
	default:
		return history.Op{Client: c.ID, Op: "delete", Key: key}
	}
}

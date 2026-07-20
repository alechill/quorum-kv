// Package history defines the client-side operation record. Every entry is
// written by the client that issued the operation — invocation time, response
// time, and outcome — never reconstructed from server logs.
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Outcome of one client operation.
const (
	OutcomeOK      = "ok"      // definite response received
	OutcomeFailed  = "failed"  // definitely did not take effect (e.g. connection refused, no_leader)
	OutcomeUnknown = "unknown" // ambiguous: may or may not have taken effect (timeout, proxy error)
)

// Op is one recorded client operation.
type Op struct {
	Client  int     `json:"client"`
	Op      string  `json:"op"` // put | get | delete | cas
	Key     string  `json:"key"`
	Value   string  `json:"value,omitempty"`  // put/cas: value written
	Expect  *string `json:"expect,omitempty"` // cas only
	StartNs int64   `json:"start_ns"`         // invocation timestamp (monotonic-based)
	EndNs   int64   `json:"end_ns"`           // response timestamp
	Outcome string  `json:"outcome"`
	// Response fields (valid when Outcome == ok):
	Ok      bool   `json:"ok"`                // cas: swap won. others: true
	Present bool   `json:"present"`           // get: key existed; delete: existed before
	RetVal  string  `json:"ret_val,omitempty"` // get: value read
	Node    string `json:"node,omitempty"`    // endpoint the op was sent to (informational)
}

// WriteFile writes ops as JSONL.
func WriteFile(path string, ops []Op) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, op := range ops {
		b, err := json.Marshal(op)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return w.Flush()
}

// ReadFile reads a JSONL history.
func ReadFile(path string) ([]Op, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f)
}

func Read(r io.Reader) ([]Op, error) {
	var ops []Op
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var op Op
		if err := json.Unmarshal(sc.Bytes(), &op); err != nil {
			return nil, fmt.Errorf("history line %d: %w", line, err)
		}
		ops = append(ops, op)
	}
	return ops, sc.Err()
}

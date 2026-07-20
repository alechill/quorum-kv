package kv

import (
	"encoding/json"
	"io"
	"sync"

	"github.com/hashicorp/raft"
)

// FSM is the replicated key-value state machine. All mutations arrive via
// Raft's Apply on committed log entries; the internal map is never written
// from anywhere else.
type FSM struct {
	mu   sync.RWMutex
	data map[string]string
}

func NewFSM() *FSM {
	return &FSM{data: make(map[string]string)}
}

// Apply executes a committed log entry. Runs serially per Raft's contract.
func (f *FSM) Apply(l *raft.Log) interface{} {
	cmd, err := DecodeCommand(l.Data)
	if err != nil {
		// A corrupt entry would mean divergence; fail loudly.
		panic("fsm: undecodable log entry: " + err.Error())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch cmd.Op {
	case OpPut:
		f.data[cmd.Key] = cmd.Value
		return Result{Ok: true}
	case OpGet:
		v, ok := f.data[cmd.Key]
		return Result{Ok: true, Present: ok, Value: v}
	case OpDelete:
		_, existed := f.data[cmd.Key]
		delete(f.data, cmd.Key)
		return Result{Ok: true, Present: existed}
	case OpCAS:
		cur, present := f.data[cmd.Key]
		matches := (cmd.Expect == nil && !present) || (cmd.Expect != nil && present && cur == *cmd.Expect)
		if matches {
			f.data[cmd.Key] = cmd.Value
			return Result{Ok: true}
		}
		return Result{Ok: false, Present: present, Value: cur}
	default:
		panic("fsm: unknown op " + cmd.Op)
	}
}

// Dump returns a copy of the current state plus nothing else — used only by
// the debug /internal/state endpoint for the harness convergence check.
func (f *FSM) Dump() map[string]string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]string, len(f.data))
	for k, v := range f.data {
		out[k] = v
	}
	return out
}

// Snapshot implements raft.FSM. State is small; copy under lock.
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	return &fsmSnapshot{state: f.Dump()}, nil
}

// Restore implements raft.FSM: replace state from a snapshot stream.
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	var m map[string]string
	if err := json.NewDecoder(rc).Decode(&m); err != nil {
		return err
	}
	f.mu.Lock()
	f.data = m
	f.mu.Unlock()
	return nil
}

type fsmSnapshot struct {
	state map[string]string
}

func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	if err := json.NewEncoder(sink).Encode(s.state); err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *fsmSnapshot) Release() {}

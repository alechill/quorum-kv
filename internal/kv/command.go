// Package kv defines the replicated state machine: the command format that
// goes through the Raft log, and the FSM that applies it.
package kv

import "encoding/json"

// Op kinds. Every client-visible operation — including Get — is a log entry,
// so a successful response always implies majority commit and reads are
// linearizable by construction.
const (
	OpPut    = "put"
	OpGet    = "get"
	OpDelete = "delete"
	OpCAS    = "cas"
)

// Command is the payload of a Raft log entry.
type Command struct {
	Op     string  `json:"op"`
	Key    string  `json:"key"`
	Value  string  `json:"value,omitempty"`
	Expect *string `json:"expect"` // CAS only: nil means "expect key absent"
}

// Result is what the FSM returns from Apply.
type Result struct {
	Ok      bool   `json:"ok"`      // CAS: swap happened. Put/Delete/Get: always true.
	Present bool   `json:"present"` // Get: key existed. Delete: key existed before.
	Value   string `json:"value"`   // Get: current value (when Present).
}

func (c Command) Encode() []byte {
	b, err := json.Marshal(c)
	if err != nil {
		panic(err) // Command is plain data; cannot fail
	}
	return b
}

func DecodeCommand(b []byte) (Command, error) {
	var c Command
	err := json.Unmarshal(b, &c)
	return c, err
}

// Package checker verifies recorded client histories for linearizability
// using Porcupine with a per-key register model supporting put/get/delete/cas.
//
// Ambiguity handling (the Jepsen "info" convention, adapted to Porcupine):
// operations recorded with outcome=unknown may or may not have taken effect.
// Their response time is extended past the end of the history, so the checker
// may linearize them at any point after invocation — including after every
// observed read, which is equivalent to "never took visible effect". Their
// output is matched permissively in the model. Operations with
// outcome=failed are excluded entirely: the client received a definite
// not-applied error, so they constrain nothing. Reads with unknown outcome
// carry no information and are likewise excluded.
package checker

import (
	"fmt"
	"time"

	"github.com/anishathalye/porcupine"

	"github.com/alechill/quorum/internal/history"
)

type kvInput struct {
	Op     string
	Key    string
	Val    string
	Expect *string
}

type kvOutput struct {
	Unknown bool
	Ok      bool
	Present bool
	Val     string
}

type kvState struct {
	Present bool
	Val     string
}

func stateMatches(st kvState, expect *string) bool {
	if expect == nil {
		return !st.Present
	}
	return st.Present && st.Val == *expect
}

// Model is the per-key linearizable register.
var Model = porcupine.Model{
	Partition: func(ops []porcupine.Operation) [][]porcupine.Operation {
		byKey := map[string][]porcupine.Operation{}
		for _, op := range ops {
			k := op.Input.(kvInput).Key
			byKey[k] = append(byKey[k], op)
		}
		parts := make([][]porcupine.Operation, 0, len(byKey))
		for _, p := range byKey {
			parts = append(parts, p)
		}
		return parts
	},
	Init: func() interface{} { return kvState{} },
	Step: func(state, input, output interface{}) (bool, interface{}) {
		st := state.(kvState)
		in := input.(kvInput)
		out := output.(kvOutput)
		switch in.Op {
		case "put":
			// A put linearized here always applies. Unknown outcome is
			// consistent (window extends past history end, so "lost before
			// commit" == "linearized after everything").
			return true, kvState{Present: true, Val: in.Val}
		case "delete":
			return true, kvState{}
		case "get":
			ok := out.Present == st.Present && (!st.Present || out.Val == st.Val)
			return ok, st
		case "cas":
			matches := stateMatches(st, in.Expect)
			if out.Unknown {
				if matches {
					return true, kvState{Present: true, Val: in.Val}
				}
				return true, st
			}
			if out.Ok {
				return matches, kvState{Present: true, Val: in.Val}
			}
			return !matches, st
		default:
			return false, st
		}
	},
	Equal: func(a, b interface{}) bool { return a.(kvState) == b.(kvState) },
	DescribeOperation: func(input, output interface{}) string {
		in := input.(kvInput)
		out := output.(kvOutput)
		exp := "<absent>"
		if in.Expect != nil {
			exp = *in.Expect
		}
		switch in.Op {
		case "put":
			return fmt.Sprintf("put(%s, %s) -> %s", in.Key, in.Val, describeOut(out))
		case "get":
			return fmt.Sprintf("get(%s) -> %s", in.Key, describeOut(out))
		case "delete":
			return fmt.Sprintf("delete(%s) -> %s", in.Key, describeOut(out))
		case "cas":
			return fmt.Sprintf("cas(%s, %s => %s) -> %s", in.Key, exp, in.Val, describeOut(out))
		}
		return "?"
	},
}

func describeOut(out kvOutput) string {
	if out.Unknown {
		return "unknown"
	}
	if out.Val != "" || out.Present {
		return fmt.Sprintf("ok=%v present=%v val=%q", out.Ok, out.Present, out.Val)
	}
	return fmt.Sprintf("ok=%v", out.Ok)
}

// ToOperations converts recorded ops into Porcupine operations, applying the
// unknown-outcome window extension. Failed ops and unknown reads are dropped.
func ToOperations(ops []history.Op) []porcupine.Operation {
	var maxEnd int64
	for _, op := range ops {
		if op.EndNs > maxEnd {
			maxEnd = op.EndNs
		}
		if op.StartNs > maxEnd {
			maxEnd = op.StartNs
		}
	}
	horizon := maxEnd + 1

	var out []porcupine.Operation
	for _, op := range ops {
		switch op.Outcome {
		case history.OutcomeFailed:
			continue
		case history.OutcomeUnknown:
			if op.Op == "get" {
				continue // an unanswered read constrains nothing
			}
			out = append(out, porcupine.Operation{
				ClientId: op.Client,
				Input:    kvInput{Op: op.Op, Key: op.Key, Val: op.Value, Expect: op.Expect},
				Output:   kvOutput{Unknown: true},
				Call:     op.StartNs,
				Return:   horizon,
			})
		case history.OutcomeOK:
			out = append(out, porcupine.Operation{
				ClientId: op.Client,
				Input:    kvInput{Op: op.Op, Key: op.Key, Val: op.Value, Expect: op.Expect},
				Output:   kvOutput{Ok: op.Ok, Present: op.Present, Val: op.RetVal},
				Call:     op.StartNs,
				Return:   op.EndNs,
			})
		default:
			// Unclassified record: treat as unknown mutation to be safe.
			out = append(out, porcupine.Operation{
				ClientId: op.Client,
				Input:    kvInput{Op: op.Op, Key: op.Key, Val: op.Value, Expect: op.Expect},
				Output:   kvOutput{Unknown: true},
				Call:     op.StartNs,
				Return:   horizon,
			})
		}
	}
	return out
}

// Result of a linearizability check.
type Result struct {
	Linearizable bool
	Ops          int // operations actually checked (after exclusions)
	Info         porcupine.LinearizationInfo
}

// Check verifies a recorded history. timeout bounds the search; a timeout is
// reported as NOT linearizable (porcupine.Unknown -> false here) so it can
// never mask a violation.
func Check(ops []history.Op, timeout time.Duration) Result {
	pops := ToOperations(ops)
	res, info := porcupine.CheckOperationsVerbose(Model, pops, timeout)
	return Result{
		Linearizable: res == porcupine.Ok,
		Ops:          len(pops),
		Info:         info,
	}
}

package harness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// FaultLog records what the injector actually did, with wall-clock stamps.
type FaultEvent struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail,omitempty"`
}

// Faults injects real faults: docker SIGKILLs and iptables partitions
// imposed inside the composed containers (network-level, not in-process).
type Faults struct {
	Nodes  []Node
	Events []FaultEvent
	Logf   func(format string, args ...interface{})
}

func (f *Faults) record(action, target, detail string) {
	f.Events = append(f.Events, FaultEvent{At: time.Now(), Action: action, Target: target, Detail: detail})
	f.Logf("fault: %s %s %s", action, target, detail)
}

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Kill sends SIGKILL to the node's container.
func (f *Faults) Kill(n Node) error {
	out, err := run("docker", "kill", "-s", "KILL", n.Container)
	f.record("kill", n.ID, out)
	return err
}

// Start restarts a previously killed container (same volumes, same IP).
func (f *Faults) Start(n Node) error {
	out, err := run("docker", "start", n.Container)
	f.record("start", n.ID, out)
	return err
}

// Isolate imposes a bidirectional network partition between node n and every
// other node, via iptables DROP rules inside each container. Client access
// (published ports, via the bridge gateway) is left untouched, so the harness
// can still probe the isolated node directly.
func (f *Faults) Isolate(n Node) error {
	var firstErr error
	for _, peer := range f.Nodes {
		if peer.ID == n.ID {
			continue
		}
		cmds := [][]string{
			{"docker", "exec", n.Container, "iptables", "-I", "INPUT", "-s", peer.IP, "-j", "DROP"},
			{"docker", "exec", n.Container, "iptables", "-I", "OUTPUT", "-d", peer.IP, "-j", "DROP"},
			{"docker", "exec", peer.Container, "iptables", "-I", "INPUT", "-s", n.IP, "-j", "DROP"},
			{"docker", "exec", peer.Container, "iptables", "-I", "OUTPUT", "-d", n.IP, "-j", "DROP"},
		}
		for _, c := range cmds {
			if out, err := run(c[0], c[1:]...); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("%v: %s: %w", c, out, err)
			}
		}
	}
	f.record("partition", n.ID, "isolated from peers (iptables DROP both directions)")
	return firstErr
}

// HealAll flushes iptables in every container and starts any stopped ones.
func (f *Faults) HealAll() {
	for _, n := range f.Nodes {
		_, _ = run("docker", "start", n.Container) // no-op if running
		for _, chain := range []string{"INPUT", "OUTPUT"} {
			_, _ = run("docker", "exec", n.Container, "iptables", "-F", chain)
		}
	}
	f.record("heal", "all", "iptables flushed, all containers started")
}

// LeaderID asks each node who the leader is and returns the first answer.
func (f *Faults) LeaderID() string {
	client := &http.Client{Timeout: 2 * time.Second}
	for _, n := range f.Nodes {
		resp, err := client.Get(n.URL + "/healthz")
		if err != nil {
			continue
		}
		var h struct {
			LeaderID string `json:"leader_id"`
		}
		err = json.NewDecoder(resp.Body).Decode(&h)
		resp.Body.Close()
		if err == nil && h.LeaderID != "" {
			return h.LeaderID
		}
	}
	return ""
}

func (f *Faults) NodeByID(id string) *Node {
	for i := range f.Nodes {
		if f.Nodes[i].ID == id {
			return &f.Nodes[i]
		}
	}
	return nil
}

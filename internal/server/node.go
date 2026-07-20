// Package server wires a Raft node (hashicorp/raft + boltdb durable log)
// to the HTTP API.
package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	boltdb "github.com/hashicorp/raft-boltdb/v2"

	"github.com/alechill/quorum/internal/kv"
)

// Peer describes one cluster member: its Raft identity/address and the HTTP
// base URL used for leader proxying.
type Peer struct {
	ID       string
	RaftAddr string
	HTTPURL  string
}

// ParsePeers parses "id@raftaddr@httpurl,..." (e.g.
// "node1@node1:7000@http://node1:8080,node2@...").
func ParsePeers(s string) ([]Peer, error) {
	var peers []Peer
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bits := strings.Split(part, "@")
		if len(bits) != 3 {
			return nil, fmt.Errorf("bad peer spec %q (want id@raftaddr@httpurl)", part)
		}
		peers = append(peers, Peer{ID: bits[0], RaftAddr: bits[1], HTTPURL: bits[2]})
	}
	if len(peers) != 3 {
		return nil, fmt.Errorf("cluster is fixed at three nodes; got %d peers", len(peers))
	}
	return peers, nil
}

// Config for one node.
type Config struct {
	NodeID        string
	DataDir       string
	RaftBind      string // listen address, e.g. ":7000"
	RaftAdvertise string // dialable address of this node, e.g. "node1:7000" or "[fdaa::x]:7000"
	Peers         []Peer
	Bootstrap     bool
}

// Node is a running Raft member.
type Node struct {
	Raft *raft.Raft
	FSM  *kv.FSM
	Cfg  Config
}

// Timing constants — documented in the README.
const (
	HeartbeatTimeout   = 1000 * time.Millisecond
	ElectionTimeout    = 1000 * time.Millisecond
	LeaderLeaseTimeout = 500 * time.Millisecond
	CommitTimeout      = 50 * time.Millisecond
	ApplyTimeout       = 5 * time.Second
)

// NewNode starts Raft with a durable boltdb log + stable store and file
// snapshots under cfg.DataDir. Safe to call on a fresh or recovering node:
// with existing state, BootstrapCluster is a no-op (ErrCantBootstrap).
func NewNode(cfg Config) (*Node, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}

	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(cfg.NodeID)
	rc.HeartbeatTimeout = HeartbeatTimeout
	rc.ElectionTimeout = ElectionTimeout
	rc.LeaderLeaseTimeout = LeaderLeaseTimeout
	rc.CommitTimeout = CommitTimeout
	rc.SnapshotInterval = 30 * time.Second
	rc.SnapshotThreshold = 4096
	rc.LogOutput = os.Stderr

	advAddr, err := net.ResolveTCPAddr("tcp", cfg.RaftAdvertise)
	if err != nil {
		return nil, fmt.Errorf("resolve advertise %q: %w", cfg.RaftAdvertise, err)
	}
	transport, err := raft.NewTCPTransport(cfg.RaftBind, advAddr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("raft transport: %w", err)
	}

	// One bolt store serves as both LogStore and StableStore; bolt commits
	// fsync, so acknowledged entries survive kill -9.
	store, err := boltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft.db"))
	if err != nil {
		return nil, fmt.Errorf("bolt store: %w", err)
	}
	snaps, err := raft.NewFileSnapshotStore(cfg.DataDir, 2, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("snapshot store: %w", err)
	}

	fsm := kv.NewFSM()
	r, err := raft.NewRaft(rc, fsm, store, store, snaps, transport)
	if err != nil {
		return nil, fmt.Errorf("new raft: %w", err)
	}

	if cfg.Bootstrap {
		var servers []raft.Server
		for _, p := range cfg.Peers {
			servers = append(servers, raft.Server{
				ID:      raft.ServerID(p.ID),
				Address: raft.ServerAddress(p.RaftAddr),
			})
		}
		f := r.BootstrapCluster(raft.Configuration{Servers: servers})
		if err := f.Error(); err != nil && err != raft.ErrCantBootstrap {
			return nil, fmt.Errorf("bootstrap: %w", err)
		}
	}

	return &Node{Raft: r, FSM: fsm, Cfg: cfg}, nil
}

// LeaderHTTPURL returns the HTTP base URL of the current leader, or "" if
// unknown or the leader is this node.
func (n *Node) LeaderHTTPURL() string {
	_, id := n.Raft.LeaderWithID()
	if id == "" || string(id) == n.Cfg.NodeID {
		return ""
	}
	for _, p := range n.Cfg.Peers {
		if p.ID == string(id) {
			return p.HTTPURL
		}
	}
	return ""
}

// IsLeader reports whether this node currently believes it is leader.
func (n *Node) IsLeader() bool {
	return n.Raft.State() == raft.Leader
}

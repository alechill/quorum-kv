// Package harness drives the composed three-node cluster with concurrent
// randomized clients, injects real faults (container kills, network-level
// partitions), records full client-side histories, and checks them with the
// linearizability checker.
package harness

import (
	"fmt"
	"strings"
)

// Node describes one cluster member as seen by the harness.
type Node struct {
	ID        string // e.g. node1
	URL       string // client-facing base URL, e.g. http://localhost:8081
	Container string // docker container name, e.g. quorum-node1
	IP        string // address on the compose network, e.g. 172.28.0.11
}

// ParseNodes parses "id=url=container=ip,...".
func ParseNodes(s string) ([]Node, error) {
	var nodes []Node
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bits := strings.Split(part, "=")
		if len(bits) != 4 {
			return nil, fmt.Errorf("bad node spec %q (want id=url=container=ip)", part)
		}
		nodes = append(nodes, Node{ID: bits[0], URL: bits[1], Container: bits[2], IP: bits[3]})
	}
	if len(nodes) != 3 {
		return nil, fmt.Errorf("expected exactly 3 nodes, got %d", len(nodes))
	}
	return nodes, nil
}

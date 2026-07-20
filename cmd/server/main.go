// quorum server: one node of the three-node replicated KV store.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/alechill/quorum/internal/server"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	nodeID := os.Getenv("NODE_ID")
	if nodeID == "" {
		log.Fatal("NODE_ID is required")
	}
	peersSpec := os.Getenv("PEERS")
	if peersSpec == "" {
		log.Fatal("PEERS is required (id@raftaddr@httpurl,...)")
	}
	peers, err := server.ParsePeers(peersSpec)
	if err != nil {
		log.Fatalf("parse PEERS: %v", err)
	}

	cfg := server.Config{
		NodeID:        nodeID,
		DataDir:       env("DATA_DIR", "/data"),
		RaftBind:      env("RAFT_BIND", ":7000"),
		RaftAdvertise: env("RAFT_ADVERTISE", nodeID+":7000"),
		Peers:         peers,
		Bootstrap:     env("BOOTSTRAP", "true") == "true",
	}

	node, err := server.NewNode(cfg)
	if err != nil {
		log.Fatalf("start node: %v", err)
	}

	httpAddr := env("HTTP_ADDR", ":8080")
	log.Printf("quorum node %s: raft on %s (advertise %s), http on %s",
		cfg.NodeID, cfg.RaftBind, cfg.RaftAdvertise, httpAddr)
	if err := http.ListenAndServe(httpAddr, server.NewHandler(node)); err != nil {
		log.Fatalf("http: %v", err)
	}
}

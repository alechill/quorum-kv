// quorum harness: drives the composed cluster with concurrent randomized
// clients under real faults, records client-side histories, and verifies
// linearizability. Also usable as a standalone checker: -check <file>.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/alechill/quorum/internal/checker"
	"github.com/alechill/quorum/internal/harness"
	"github.com/alechill/quorum/internal/history"
)

func main() {
	var (
		nodesSpec = flag.String("nodes",
			"node1=http://localhost:8081=quorum-node1=172.28.0.11,"+
				"node2=http://localhost:8082=quorum-node2=172.28.0.12,"+
				"node3=http://localhost:8083=quorum-node3=172.28.0.13",
			"node spec: id=url=container=ip,...")
		duration   = flag.Duration("duration", 90*time.Second, "chaos phase duration")
		clients    = flag.Int("clients", 8, "concurrent workload clients")
		keys       = flag.Int("keys", 8, "workload key space size")
		seed       = flag.Int64("seed", time.Now().UnixNano(), "randomness seed")
		outDir     = flag.String("out", "harness/out", "output directory (history, report)")
		fixtures   = flag.String("fixtures", "harness/fixtures", "fixtures directory")
		skipFaults = flag.Bool("skip-faults", false, "traffic only, no fault injection")
		checkFile  = flag.String("check", "", "standalone mode: check a history file and exit")
	)
	flag.Parse()

	if *checkFile != "" {
		ops, err := history.ReadFile(*checkFile)
		if err != nil {
			log.Fatalf("read history: %v", err)
		}
		res := checker.Check(ops, 5*time.Minute)
		fmt.Printf("history: %d ops, %d checkable, linearizable=%v\n", len(ops), res.Ops, res.Linearizable)
		if !res.Linearizable {
			os.Exit(1)
		}
		return
	}

	nodes, err := harness.ParseNodes(*nodesSpec)
	if err != nil {
		log.Fatal(err)
	}
	report, err := harness.Run(harness.Options{
		Nodes:       nodes,
		Duration:    *duration,
		NumClients:  *clients,
		NumKeys:     *keys,
		Seed:        *seed,
		OutDir:      *outDir,
		FixturesDir: *fixtures,
		SkipFaults:  *skipFaults,
	})
	if err != nil {
		log.Fatalf("harness: %v", err)
	}
	if !report.AllPassed {
		os.Exit(1)
	}
}

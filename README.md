# quorum — replicated key-value store

Three-node replicated KV store with one logical HTTP API and a guarantee:
**an acknowledged write survives the loss of any single node, and the
cluster never lies to a reader.** Consensus is Raft
([hashicorp/raft](https://github.com/hashicorp/raft)); every node persists
its own log (bolt, fsync-on-commit) — there is no shared datastore.
Correctness is demonstrated by a fault-injecting harness whose recorded
client histories are verified by a linearizability checker
([Porcupine](https://github.com/anishathalye/porcupine)).

Public deployment: three machines on Fly.io, https://quorum-kv-frontier.fly.dev
(see *Deployment* below).

## Run it

On a machine with only Docker:

```sh
docker compose up -d --build
```

That brings up `node1..node3` (published on `localhost:8081..8083`), each an
independently killable/restartable container with its own volume. This
composed environment is canonical for verification.

## API

Any node accepts any operation; non-leaders transparently proxy to the
leader (send `X-Quorum-No-Proxy: 1` to get a `421 not_leader` rejection
instead — the harness uses this to prove a partitioned minority refuses
writes). Keys and values are strings.

```sh
curl -X PUT  localhost:8081/kv/mykey  -d '{"value":"v1"}'   # {"ok":true}
curl         localhost:8082/kv/mykey                        # {"ok":true,"present":true,"value":"v1"}
curl -X POST localhost:8083/kv/mykey/cas -d '{"expect":"v1","value":"v2"}'  # {"ok":true}
curl -X POST localhost:8081/kv/mykey/cas -d '{"expect":null,"value":"x"}'   # cas-if-absent -> {"ok":false,...}
curl -X DELETE localhost:8081/kv/mykey                      # {"ok":true,"present":true}
```

**Linearizability:** every operation — reads included — is a Raft log entry.
A `200` means the entry was committed on a majority and applied; reads can
never return stale or uncommitted values. A leader cut off from the majority
steps down when its lease lapses and fails in-flight proposals; it never
acknowledges without quorum.

Error contract (what the harness's outcome classification relies on):

| Response | Meaning | Effect |
|---|---|---|
| `503 no_leader`, `421 not_leader`, `400 bad_request` | definitely **not** applied | safe to retry |
| `500 apply_failed`, `502 proxy_error`, timeouts | **ambiguous** — may have applied | recorded as unknown-outcome |

## Verification harness

```sh
docker compose up -d --build
go run ./cmd/harness            # full suite: ~60s chaos + checks
go run ./cmd/harness -check some-history.jsonl   # standalone checker
```

The harness runs, in order:

1. **Checker self-test** — the committed known non-linearizable history
   (`harness/fixtures/known-bad-history.jsonl`: a stale read strictly after
   an acknowledged overwrite) MUST be rejected, and a known-good history
   accepted. A checker that passes everything is not a checker.
2. **Reset + smoke** — recorded deletes of all workload keys; put/get via
   every node's endpoint.
3. **CAS contention** — concurrent `cas` with the same expected value:
   exactly one winner per round.
4. **Leader-kill durability** — write, ack, `docker kill -s KILL` the leader
   immediately, read the value back.
5. **Minority refuses writes** — the leader is partitioned off with iptables;
   direct (no-proxy) writes to it must never be acknowledged while the
   majority side elects and keeps serving.
6. **Chaos** — concurrent randomized `put/get/cas/delete` clients while the
   injector kills containers (leader included, real `SIGKILL`) and imposes /
   heals network-level partitions (iptables `DROP` inside the containers, on
   the composed network — never in-process flags).
7. **Convergence** — after healing, all three nodes must reach identical
   committed state without operator action.
8. **Linearizability** — the full client-recorded history (invocation,
   response, timing — recorded by the clients themselves, never
   reconstructed from server logs) is verified with Porcupine against a
   per-key register model. Ambiguous outcomes are checked under both
   "applied" and "never applied" interpretations (open-ended windows).

Outputs land in `harness/out/`: `history.jsonl` (full client history),
`report.json` (per-check results), and a violation visualization HTML if the
check ever fails.

## Timing parameters

Documented per the brief (values in `internal/server/node.go`):

| Parameter | Value |
|---|---|
| Heartbeat timeout | 1000 ms |
| Election timeout | 1000 ms |
| Leader lease timeout | 500 ms |
| Commit timeout | 50 ms |
| Apply (proposal) timeout | 5 s |
| Snapshot interval / threshold | 30 s / 4096 entries |

## CI

GitHub Actions builds the project and runs the **full harness — faults
included —** on every push to `main`, then (only if green) deploys to
Fly.io and runs the post-deploy smoke check. See
`.github/workflows/ci.yml`.

## Deployment

Three Fly.io machines built from the same container image, one per process
group (`node1/node2/node3`), each with its own volume; Raft runs over Fly
6PN private networking (node identity and peer DNS names are derived from
Fly runtime env in `docker-entrypoint.sh`). Deploys are automated: CI runs
`flyctl deploy` on every green push to `main` — no manual step.

The post-deploy smoke check (`scripts/fly-smoke.sh`) writes a key through
one machine's public endpoint and reads it back through a different
machine's endpoint using the `fly-force-instance-id` header. It kills no
nodes: production is held to the smoke check and to image parity with what
the harness verified.

```sh
# talk to the public deployment, pinning a specific machine:
curl -X PUT https://quorum-kv-frontier.fly.dev/kv/hello \
  -H "fly-force-instance-id: <machine-id>" -d '{"value":"world"}'
curl https://quorum-kv-frontier.fly.dev/kv/hello \
  -H "fly-force-instance-id: <other-machine-id>"
```

## Design notes

- **Fixed membership** of three nodes; no dynamic membership, no Byzantine
  tolerance, single-key operations only.
- **Reads through the log:** the simplest read path that can never serve
  stale data; read-index/lease optimizations were deliberately not taken.
- **Client redirect behavior:** server-side proxy to the leader (choice per
  brief), with `X-Quorum-No-Proxy` opt-out and a one-hop loop guard.
- **Snapshots** exist to keep restarts workable (bounded log replay), not as
  a compaction feature.
- `/internal/state` is a debug endpoint (local FSM dump) used only by the
  harness convergence check; it is not part of the client API and makes no
  linearizability claims.

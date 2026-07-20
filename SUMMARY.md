# Session summary — quorum

Delivered the full PRD in one session (~3h14m wall clock, of which ~2h25m was
waiting out a GitHub-wide Actions incident).

## What was built

- **Store**: three-node replicated KV store in Go on Raft (`hashicorp/raft` +
  `raft-boltdb`); each node persists its own fsync'd log and snapshots on its
  own volume — no shared datastore. Every operation (reads included) goes
  through the Raft log, so acknowledgements imply majority commit and reads
  are linearizable by construction. Non-leaders proxy to the leader
  (`X-Quorum-No-Proxy` opts out). API: `put`/`get`/`delete`/`cas` on
  `/kv/{key}`.
- **Canonical environment**: `docker compose up -d --build` brings up three
  independently killable containers with static IPs and NET_ADMIN (for real
  network-level partitions).
- **Verification harness** (`go run ./cmd/harness`): concurrent randomized
  clients recording full client-side histories; fault injector doing real
  `docker kill -s KILL` (leader included) and iptables partitions imposed
  inside the containers; explicit probes for leader-kill durability,
  minority write-refusal, majority liveness, CAS single-winner; convergence
  check; Porcupine linearizability check over the full history with sound
  unknown-outcome handling. The committed known non-linearizable history
  (`harness/fixtures/known-bad-history.jsonl`) is rejected by the checker on
  every run (plus unit tests covering impossible-CAS and double-winner
  histories).
- **CI**: GitHub Actions on every push to `main` — unit tests, compose
  bring-up, **full fault harness**, then (canonical repo only) Fly.io deploy
  and a cross-node smoke check. Green on push at time of writing.
- **Deployment**: three Fly.io machines (`quorum-kv-frontier`, lhr) from the
  same image, one per process group, Raft over 6PN private networking,
  volumes per node. Deploys are automated by CI; the post-deploy smoke check
  writes through one machine's public endpoint and reads through another's
  (`fly-force-instance-id`).

## Where things live

- Canonical public repo (CI + deploys): https://github.com/alechill/quorum-kv
- This repo (experiment origin): pushed to `shftwst/faff-lab-experiments-quorum-one-shot` (verify-only CI)
- Live: https://quorum-kv-frontier.fly.dev (`/healthz`, `/kv/{key}`)

## Verification evidence

- Local: repeated full-fault harness runs green (11 checks; ~12K ops/run;
  histories linearizable; known-bad history rejected).
- CI: run 29713076252 (dispatch) and 29714701721 (push) both green end-to-end
  — harness with faults on the runner, deploy, cross-node smoke.

## Bumps along the way

1. **Proxy body bug** (only code defect found): the JSON decoder consumed the
   request body before leader-proxying; fixed by buffering.
2. **Checker false positive**: prior-run volume residue read at run start —
   fixed with a recorded reset phase deleting all workload keys; values also
   made unique across runs.
3. **GitHub Actions incident**: every workflow run (including a trivial
   echo-only probe) died with `startup_failure` for ~2.5h; confirmed
   platform-side via status page, waited it out with a re-dispatching
   monitor, then everything ran green without workflow changes.

Per-session token economics: see `ECONOMICS.md` / `economics.json`.
Redacted transcript: `transcripts/`.

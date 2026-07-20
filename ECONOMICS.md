# Economics report — quorum one-shot session

**Figures are self-measured from the session transcript** (the most recently
modified `*.jsonl` under `~/.claude/projects/` for this working directory,
copied to `transcripts/66eb5ae0-849c-40cd-893e-c13a1ed004c6.jsonl`). Per-message
`usage` fields were summed across the whole session. **This bookkeeping step
and the final message are necessarily excluded from their own totals** — usage
they generate lands after the measurement snapshot. **The transcript is the
authoritative source for verification.** Machine-readable copy:
`economics.json`. Tokens only; no currency conversion.

## Session

| | |
|---|---|
| Start | 2026-07-20T00:17:11Z |
| End (at measurement) | 2026-07-20T03:31:31Z |
| Wall clock | 3h 14m |
| Model | `claude-fable-5` |
| Harness | Claude Code 2.1.215 |
| Assistant turns (with usage) | 318 |
| Subagents | none spawned (background bash tasks/monitors log into the same transcript) |

## Totals (measured, reported separately)

| Usage field | Tokens |
|---|---|
| `input_tokens` (uncached) | 635 |
| `output_tokens` | 301,325 |
| `cache_creation_input_tokens` | 371,105 |
| `cache_read_input_tokens` | 44,754,664 |

## By phase (labeled by observed activity)

| Phase | Window (UTC) | Wall | Turns | Output | Cache create | Cache read |
|---|---|---|---|---|---|---|
| 1. Reading brief & environment survey | 00:17–00:21 | 3.8m | 26 | 54,976 | 112,388 | 1,206,660 |
| 2. Core implementation (server, checker, harness code) | 00:21–00:31 | 10.5m | 38 | 96,378 | 68,848 | 3,158,496 |
| 3. Local bring-up & API proxy bugfix | 00:31–00:33 | 1.5m | 15 | 8,000 | 10,180 | 1,646,289 |
| 4. Fault-harness runs & linearizability debugging | 00:33–00:47 | 13.7m | 42 | 36,662 | 35,910 | 5,105,367 |
| 5. Fly.io setup, GitHub repo, CI authoring, first deploy + smoke | 00:47–00:58 | 11.5m | 70 | 47,831 | 55,136 | 10,247,727 |
| 6. GitHub Actions outage: diagnosis & waiting | 00:58–03:23 | 144.8m | 97 | 42,866 | 58,027 | 17,330,786 |
| 7. CI green verification (dispatch + push runs) | 03:23–03:31 | 8.5m | 30 | 14,612 | 30,616 | 6,059,339 |

(`input_tokens` per phase are 30–194 each — negligible; see `economics.json`.)

## By tool

Result token weights are **approximations** (payload chars ÷ 4) — the only
estimated figures in this report; call counts are exact.

| Tool | Calls | Result tokens (approx) |
|---|---|---|
| Bash | 76 | ~11,800 |
| Write | 28 | ~1,500 |
| Edit | 14 | ~760 |
| TaskUpdate | 12 | ~60 |
| Monitor | 8 | ~420 |
| TaskCreate | 8 | ~180 |
| ToolSearch | 3 | ~70 |
| Read | 2 | ~2,500 |
| TaskStop | 1 | ~440 |

## Waste analysis (most tokens for least progress)

1. **GitHub Actions platform outage** (00:58–03:23, external): 96 turns,
   42,123 output tokens, 17.2M cache-read tokens, ~2h25m wall clock. Every
   push and dispatch ended in `startup_failure` during a GitHub-wide Actions
   incident (confirmed via status page and a trivial echo-only probe workflow
   that also failed at startup). Cost was mostly monitor wakeups and
   diagnosis; the diagnosis itself was necessary to rule out a workflow
   defect. Roughly a third of the session's output tokens and nearly half its
   cache reads bought zero forward progress here.
2. **Linearizability false-positive debug & rework** (00:35–00:46): 30 turns,
   28,218 output tokens. A chaos-run "violation" was actually pre-run volume
   residue read at run start (checker's model assumes an empty register).
   Included per-key bisection, one 90s run that died silently (suspected
   checker OOM on an oversized history), the recorded-reset-phase fix, and
   re-verification. About half was productive (a real harness improvement
   emerged); half was the misdiagnosis path.
3. **Repo remote confusion + GitHub API 503 retries** (00:48–00:51): 12
   turns, 8,746 output tokens. First push went to the wrong remote (a
   pre-existing origin was overlooked), and secret-setting hit repeated 503s
   (early signs of the same incident).

Everything else was first-pass productive: the store, checker, and harness
worked on their second full run (the only code defect found all session was
the proxy body-consumption bug, fixed in one edit cycle).

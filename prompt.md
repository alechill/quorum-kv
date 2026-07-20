# Quorum experiment

## Environment

You have been provided with a minimal git repo with .env.claude-box env var configuration, and are running inside a claude-box container (containerised claude-code) with applicable CLIs and packages installed. You may install other packages as necesary.

## Task

Read `./quorum-prd.md` and follow the PRD to deliver an application.

## Post-task (mandatory final step — do this last, after all other work is complete)

### Economics

Before ending, produce an economics report for this run and commit it as
`ECONOMICS.md` with a machine-readable `economics.json` beside it.

1. Locate this session's transcript: the most recently modified `*.jsonl` under
   `~/.claude/projects/` in the directory whose name corresponds to this working
   directory. If you cannot find or read it, state that plainly in ECONOMICS.md and
   report only what you can count directly (turns, tool calls, wall clock). Never
   present an estimated token figure as a measured one.
2. From the transcript, sum the per-message `usage` fields across the whole session:
   `input_tokens`, `output_tokens`, `cache_creation_input_tokens`,
   `cache_read_input_tokens`. Report each total separately — do not collapse them
   into one number.
3. Break the totals down two ways:
   - **By phase:** segment the session chronologically by what was actually happening
     (e.g. reading the brief, scaffolding, core implementation, verification harness,
     deployment, debugging/rework), with per-phase token subtotals, wall-clock time,
     and turn counts. Label phases by activity observed in the transcript, not by
     what was planned.
   - **By tool:** per tool (file reads, shell commands, edits, etc.), the number of
     calls and the token weight of their results.
4. Add a **waste analysis**: the segments that consumed the most tokens for the least
   progress (retry loops, failed commands, dead-end approaches), each quantified.
5. Record: total turns, session start/end timestamps, model ID, and harness + version.
6. State in the report that the figures are self-measured from the transcript, that
   this bookkeeping step and the final message are necessarily excluded from their
   own totals, and that the transcript is the authoritative source for verification.

Report tokens only — do not convert to currency; pricing is applied downstream.

### Transcripts

Locate this session's transcript and all of its subagents and copy them into a `transcripts` directory in this git repo. Secret scan before committing: check every value from .env.claude-box and common token shapes and redact them before committing.

### Summary

Produce and commit a short summary of the session to `SUMMARY.md`.

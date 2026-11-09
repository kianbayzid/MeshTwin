# AI-assisted workflow

MeshTwin is built with Claude Code. This page explains how that work is organised and why, because
"how do you keep AI-written code trustworthy?" is a fair interview question.

## The short version
One **lead** session plans and integrates. **Lane agents** each own one part of the repo. An independent
**verifier** must pass every change before it counts. **Hooks** apply the few rules that must never depend
on an agent remembering them.

```
              lead (main session): plans, assigns, merges
             /            |              \              \
     go-engineer   analytics-engineer   platform-engineer   protocol-researcher
     ingest/ api/       analytics/       deploy/ CI ...       (read + web)
     schema/
             \            |              /
                      verifier  ── PASS / FAIL with evidence ──▶ lead merges
```

## Agents (`.claude/agents/`)

| Agent | Tools | Owns | Why it exists |
|---|---|---|---|
| `go-engineer` | read, edit, bash | `ingest/`, `api/`, `schema/` | The Go services and the schema they write |
| `analytics-engineer` | read, edit, bash | `analytics/` | The Python models: different toolchain, different tests |
| `platform-engineer` | read, edit, bash | `deploy/`, `dashboards/`, `.github/`, `Makefile`, Dockerfiles | Infrastructure changes have the widest blast radius, so they're kept apart from app code |
| `verifier` | read, bash, **no edit tools** | nothing | A second opinion that can't "fix" its way to green |
| `protocol-researcher` | read, web, **no edit tools** | nothing | Facts about MeshCore come from cited primary sources, not memory |

Every agent reads `CLAUDE.md` first, so they all share the same conventions, privacy rules and ownership table.

### Why lanes
- **Smaller context, better work.** An agent working in `analytics/` doesn't need the Go decoder in its head.
- **Parallelism without collisions.** Two agents never edit the same file, so their work can run at the same time.
- **Explicit contracts.** Lanes only meet at `schema/schema.json` (Go ↔ Python) and `docs/api-contract.md`
  (analytics ↔ API). Both are machine-checked: `TestSchemaMatchesJSON` in Go, a matching test in Python, and
  contract tests on the JSON. Changing a contract is a visible, deliberate event.

### Why a separate verifier
The agent that wrote the code is the worst-placed to judge it. It shares the assumptions that produced any bug,
and under pressure to finish it may "fix" a failing test by weakening it. The verifier:
- has no Edit/Write tools, and its instructions forbid writing commands, so it can only report;
- runs the whole check list (format, vet/lint, types, tests, schema, API contract, privacy greps, "were any
  tests weakened?") and pastes real output;
- treats a check it couldn't run as a failure to report, not a pass.

Limit: the verifier still has Bash, so "never edits" is an instruction, not a sandbox. What backs it up is
that its report is evidence the lead can re-run.

### Merge order
Within a phase: **analytics and API lanes first, then platform.** Platform work packages what the other
lanes produced (images, CronJobs, probes, dashboards), so it should build on verified code rather than a
moving target. The verifier runs again after each merge, because two changes that each pass alone can still
conflict.

## Hooks (`.claude/settings.json`, scripts in `.claude/hooks/`)

| Hook | When | What | Why a hook and not an instruction |
|---|---|---|---|
| `format.sh` | after every Edit/Write | `gofmt -w` for `.go`, `ruff format` for `.py`. Exit 2 (feedback to the agent) if the formatter fails, which is almost always a syntax error | Formatting is mechanical, so it shouldn't cost agent attention or review noise. Syntax errors surface at the edit, not ten steps later |
| `guard-kube.sh` | before every Bash/PowerShell command | Blocks `kubectl`/`helm` unless every named context, or the current one, is `k3d-*`. Fails closed when the context can't be determined | A wrong `kubectl apply` against a real cluster is the one mistake here that can't be undone. That must not depend on a model remembering a rule |

Both scripts are plain bash. They parse the hook's JSON with whatever Python works: on Windows, `python3` is
often a Store stub, so `lib.sh` checks each candidate actually runs. There's no `jq` dependency. Tested with
sample inputs, including bypass attempts (`--context=prod`, `use-context prod && …`, `$(kubectl …)`). The guard
is a seatbelt, not a security boundary: a script that calls kubectl internally, or `KUBECONFIG=other.yaml`, isn't
analysed, and the header says so.

## What stays human
- Phase boundaries: the work stops after each phase so the author can run it and ask questions.
- Choices with no technical default: region, data sources, privacy trade-offs, third-party images.
- Commits and anything outward-facing (connecting to a community broker, publishing).

## Trade-offs
- **Cost:** each agent starts cold and re-reads context, so small edits are cheaper done by the lead. Agents
  pay off on big, parallel, or independent work: a whole phase in one lane, research next to coding, verification.
- **Coordination overhead:** contracts must be written before the lanes start (the API contract was written
  before Phase 3 for exactly this reason).
- **Trust:** the verifier reduces but doesn't remove the need to read the code. The interview standard
  ("can I explain every line?") is still the author's job.

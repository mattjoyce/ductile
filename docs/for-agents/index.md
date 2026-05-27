# For Agents

Ductile is designed to be **operated**, not just used. If you are an AI agent — or a human telling one to operate this — this is the entry point.

## The loop

1. Load the operator skill (see [Skills](#skills), below) into your client.
2. State a goal in natural language: *"Every morning at 7am, fetch the headlines from these RSS feeds, summarize them with Fabric, and post the summary to Discord."*
3. The agent uses the `/skills` registry and the auto-generated OpenAPI surface to discover what's available; authors a pipeline; runs it; reads the logs; iterates until the goal is met.

The human role is to state goals and audit results. The agent does the wiring.

## Why this works

The five lifecycle pillars — **Run, Debug, RCA, Test, Author** — are each backed by structured affordances that an LLM can drive without reading source code:

| Pillar | Primary surfaces |
|---|---|
| Run | CLI verbs, `/skills` registry, OpenAPI |
| Debug | `/system/doctor`, `/system/selfcheck`, structured logs |
| RCA | execution ledger (SQLite), `/stopwatch/{plugin}` latency aggregation |
| Test | manifest contract, fixture conventions |
| Author | plugin protocol (stdin/stdout JSON), manifest schema |

See the [Constitution](https://github.com/mattjoyce/ductile/blob/main/CONSTITUTION.md) for the alignment paragraph and how each pillar is expected to evolve.

## Skills

Ductile ships skill manifests that give agents structured ways to operate it. Drop these into your agent's skills directory (`cp -r skills/<name>/ ~/.claude/skills/<name>/`):

### Pillar skills

- [`skills/ductile/`](https://github.com/mattjoyce/ductile/tree/main/skills/ductile) — **Pillar 1: Run.** Operate, configure, deploy.
- [`skills/ductile-rca/`](https://github.com/mattjoyce/ductile/tree/main/skills/ductile-rca) — **Pillar 3: RCA.** Root cause analysis from the execution ledger.
- [`skills/ductile-plugin-developer/`](https://github.com/mattjoyce/ductile/tree/main/skills/ductile-plugin-developer) — **Pillar 5: Author.** Build plugins to the manifest contract.

Planned: `ductile-doctor` (Pillar 2: Debug), `ductile-plugin-tester` (Pillar 4: Test).

### Discipline skills

Skills that don't run Ductile — they keep it honest.

- [`skills/surface-contract/`](https://github.com/mattjoyce/ductile/tree/main/skills/surface-contract) — **Doc/code seam audit.** Ousterhout × Liskov surface/contract discipline applied to the boundary between what the docs claim and what the code does. Run this when docs drift from reality, after a refactor that changes a public-facing API, or before a release. **The code is the reality; the docs are the contract** — this skill is how you keep them aligned.

## Planned agentic affordances

These don't exist yet but are on the roadmap:

- **`/llms.txt` and `/llms-full.txt`** at the site root — one-shot context manifests for agents that fetch documentation pages.
- **`/api.json`** — the OpenAPI surface published as a static endpoint.
- **`/schema/`** — JSON schemas for config, plugin manifest, event shape.
- **`/examples/`** — known-good pipelines, runnable and human-readable.
- **MCP server endpoint** at `ductile.run/mcp` — add ductile's docs and schemas to Claude Desktop / Cursor as an MCP context source.
- **Operator-eval scoreboard** at `/eval` — hermetic benchmark of how well Claude / Gemini / Codex / local models operate ductile against a fixed problem set.

If you are reading this as an agent and any of these are now live, prefer them over the GitHub URLs above.

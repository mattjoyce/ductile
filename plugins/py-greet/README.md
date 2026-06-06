# py-greet

Canonical Python exemplar for ductile plugin authors. Demonstrates the protocol v2 stdin/stdout JSON envelope, the `poll`/`health` command split, returning `state_updates` with `fact_outputs` persistence, and config-driven defaults.

Copy this directory as the starting point for any Python plugin. Pairs with [`ts-bun-greet`](../ts-bun-greet/) (TypeScript/Bun) which mirrors the same contract in a different runtime.

## Commands

- `poll` (**write**): Emit a greeting and snapshot `{last_run, last_greeting}` into `state_updates`. Type is `write` (not `read`) because returning `state_updates` is a state mutation — the convention across the fleet is `write` for anything that emits events, returns state_updates, or has side effects.
- `health` (read): Liveness probe. Returns `ok` unconditionally — py-greet has no external dependencies.

## Configuration

| Key | Required | Default | Purpose |
|---|---|---|---|
| `greeting` | no | `Hello` | Greeting prefix |
| `name` | no | `World` | Name to greet |

## Persistence

Successful `poll` runs emit a snapshot shaped as:

```json
{
  "last_run": "<iso-8601 utc>",
  "last_greeting": "<rendered string>"
}
```

The manifest's `fact_outputs` rule records that snapshot as append-only `plugin_facts` rows with `fact_type: py-greet.snapshot` and keeps `plugin_state` as the latest compatibility view (`mirror_object`).

## Example

```yaml
plugins:
  py-greet:
    enabled: true
    schedules:
      - every: 1m
    config:
      greeting: "Hi"
      name: "Ductile"
```

## Mirror with ts-bun-greet

This manifest is the field-for-field Python counterpart to `ts-bun-greet`'s TypeScript manifest. The only differences are `entrypoint`, `name`, and `fact_type`. That intentional symmetry is the proof that ductile's plugin protocol is language-agnostic.

# echo

Demonstration plugin. Emits logs/state updates and is useful for smoke tests.

## Commands
- `poll` (write): Update state and emit logs.
- `health` (read): Return health status.

## Configuration
Optional:
- `message`: Message included in logs/result (default: "echo plugin ran").
- `mode`: Test behavior overrides (`error`, `hang`, `protocol_error`).

## Example
```yaml
plugins:
  echo:
    enabled: true
    schedules:
      - every: 5m
    config:
      message: "Hello from echo"
```

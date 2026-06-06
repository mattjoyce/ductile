---
id: 82
status: done
priority: Low
blocked_by: []
tags: [state, dispatch, hickey, decomplect, sprint-17]
---

# Read route control-plane fields from accumulated context in one pass

**Origin: Hickey×Armstrong audit of #75, finding H3 (noted on card 81). 2026-06-06.**

The durable `AccumulatedJSON` blob was `json.Unmarshal`-ed once **per field**: a job-dispatch site
ran `PipelineInstanceIDFromAccumulated` + `RouteDepthFromAccumulated` + `RouteMaxDepthFromAccumulated`
back-to-back (`internal/dispatch/dispatcher.go` ~827 and ~870), three full parses of the same bytes
for three fields — and #75 sat a 4th reader (the predicate `Scope.Context` unmarshal) right next to
it. One durable value, many ad-hoc decoders.

**DONE.** Added `state.RouteControlPlane` + `state.RouteControlPlaneFromAccumulated`
(`internal/state/pipeline_instance.go`) which decodes all three control-plane fields in a single
pass. Both multi-field dispatcher sites now call it once instead of three times. The single-field
helpers remain for the genuinely one-field callers (`api/handlers.go:559`, `dispatcher.go` parent
lineage). The predicate `Scope.Context` unmarshal is left as-is — it needs the *whole* accumulated
map, not just the control-plane namespace, so it is a different read.

Test `TestRouteControlPlaneFromAccumulated` asserts the one-pass reader agrees field-for-field with
the single-field helpers; `TestRouteControlPlaneFromAccumulatedEmpty` covers nil / empty / no-namespace
/ malformed. Full `go build ./...` + `go test ./...` green (29 packages).

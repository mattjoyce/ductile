---
id: 138
status: backlog
priority: Medium
tags: [config, webhooks, bootstrap, validation, docs]
---

# From-scratch deadlock survives at config.Load: webhook secret_ref hard-errors before the posture decision

> Raised 2026-06-10, dual luminary code review of `feat/129-vault-operable-posture`
> (Hickey×Armstrong F4).

## The concern
`runtime.go:832-836` says skipping webhook serving in management posture "avoids a from-scratch
deadlock: a webhook secret_ref cannot resolve until it is minted, but minting needs the daemon up."
The deadlock re-enters one layer earlier: `checkSecretRef` is a **hard error** when the secret is
absent (`internal/config/validator.go:24-33`) and `validateWebhooks` (`:102-115`) requires every
endpoint's `secret_ref` to resolve — so a from-scratch config whose `webhooks.yaml` is included
from day one fails `config.Load` and never reaches `DecideBootPosture` to mint that very secret.
The migrated fixture works around it knowingly: `test/fixtures/docker/webhook-ingress/run.sh:56-57`
appends the `webhooks.yaml` include only *after* the bootstrap rung. Neither DEPLOYMENT.md nor
SECRETS.md documents this include-staging requirement.

## Fix
Either soften `checkSecretRef` to a warning when the config is in the bootstrap condition (zero api
tokens + vault present — the same predicate `DecideBootPosture` uses), or document "stage
secret_ref-bearing includes out of config.yaml until after the bootstrap rung" in DEPLOYMENT.md.
Either way, correct the `runtime.go` comment to name the load-time half of the deadlock.

## Done when
A from-scratch deploy that declares webhooks is either bootable into management posture, or its
include-staging requirement is documented in the runbook; the comment matches reality.

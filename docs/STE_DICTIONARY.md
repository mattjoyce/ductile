# Controlled Language — Technical Names and Technical Verbs

> **Diátaxis register: _reference_.** Look words up here. This page does not teach the writing
> style and it does not explain the domain. For what a term *means*, see
> [Glossary](GLOSSARY.md). For why the design is shaped that way, see the ADRs.

Ductile's primary reader is an LLM operator, with humans as auditors
([CONSTITUTION.md](https://github.com/mattjoyce/ductile/blob/main/CONSTITUTION.md)). A word that
carries two meanings costs an agent a wrong action, not merely a moment of confusion. This page is
the project dictionary for **ASD-STE100 Simplified Technical English**.

STE Part 1 permits a project to approve its own **Technical Names** (domain nouns) and **Technical
Verbs** (domain verbs) in addition to the general dictionary. This page defines only those project
additions. It does not reproduce the ASD-STE100 general dictionary, which ASD publishes and
licenses separately.

This page extends the rule already stated in `AGENTS.md` §3b: *"These terms have specific meanings.
Use them precisely; do not introduce synonyms."*

---

## 1. Where the controlled language applies

STE constrains procedure, not argument. The split follows the Diátaxis register each page already
declares:

| Register | Pages | Language |
|---|---|---|
| **How-to** | [Deployment](DEPLOYMENT.md), [Bootstrap](BOOTSTRAP.md), [CachyOS Cookbook](CACHYOS_COOKBOOK.md), runbooks | **STE.** An agent executes these. |
| **Reference** | [Config](CONFIG_REFERENCE.md), [API](API_REFERENCE.md), this page, [Glossary](GLOSSARY.md) | **STE** for entry text. Tables and schemas are exempt. |
| **Explanation** | [Deployment Postures](DEPLOYMENT_POSTURES.md), [Why](WHY.md), [8 Idioms](8_IDIOMS_OF_DUCTILE.md), ADRs | **House voice.** A human auditor reads these. Metaphor is allowed and useful. |
| **Tutorial** | [Tutorials](tutorials/bootstrap.md) | House voice, with STE for the command steps. |

Use the approved names and verbs in **every** register. Only the sentence rules in §2 are limited to
how-to and reference.

---

## 2. Writing rules

Apply these rules to how-to and reference text.

1. Write in the active voice.
2. Use the imperative for an instruction. Write `Set the mode to 0600.` Do not write `The mode
   should be set to 0600.`
3. Write one instruction in one sentence.
4. Keep a procedural sentence to 20 words or fewer. Keep a descriptive sentence to 25 words or
   fewer.
5. Write a maximum of six sentences in a descriptive paragraph.
6. Put the condition first. Write `If the boot gate refuses, read the journal.`
7. Put a warning before the step it applies to, not after.
8. Keep the article. Write `the gateway`, not `gateway`.
9. Use a maximum of three nouns in a noun cluster. Write `the state directory of the account`, not
   `the account state dir owner mode`.
10. Do not use metaphor, idiom, or rhetorical inversion in a procedure.
11. Use one term for one thing. See §5.

Approved verb forms are the infinitive, the imperative, the simple present, the simple past, and the
past participle as an adjective. Do not use a verb in the `-ing` form unless it is part of a
Technical Name, such as `Circuit Breaker`.

---

## 3. Technical Names

Nouns approved for ductile documentation. The Source column gives the page that defines the
meaning. This table approves the **word**; it does not repeat the definition.

### 3a. Runtime and execution

| Technical Name | Source | Note |
|---|---|---|
| gateway | [Glossary](GLOSSARY.md) | The `ductile` binary and the process it runs. |
| plugin | [Glossary](GLOSSARY.md) | The code and the manifest. |
| connector | [Glossary](GLOSSARY.md) | The logical integration point. Not a synonym for plugin. |
| alias | [Glossary](GLOSSARY.md) | A configured instance of a base plugin. |
| command | [Glossary](GLOSSARY.md) | `poll`, `handle`, `health`, `init`. |
| job | [Glossary](GLOSSARY.md) | The atomic unit of work. |
| queue | [Glossary](GLOSSARY.md) | The SQLite work queue. |
| worker | [Glossary](GLOSSARY.md) | One execution slot. Not an account. Not a principal. |
| worker pool | [Glossary](GLOSSARY.md) | The set of execution slots. |
| parallelism | [Glossary](GLOSSARY.md) | The per-plugin concurrent job limit. |
| schedule | [Glossary](GLOSSARY.md) | A scheduler entry. |
| jitter | [Glossary](GLOSSARY.md) | A random schedule offset. |
| circuit breaker | [Glossary](GLOSSARY.md) | The failure safety switch. |
| execution ledger | [Glossary](GLOSSARY.md) | The persistent job history. |

### 3b. Events and data

| Technical Name | Source | Note |
|---|---|---|
| event | [Glossary](GLOSSARY.md) | An immutable trigger record. |
| payload | [Glossary](GLOSSARY.md) | The JSON object on an event. |
| baggage | [Glossary](GLOSSARY.md) | Metadata carried along a pipeline. |
| pipeline | [Glossary](GLOSSARY.md) | A named series of steps. |
| event router | [Glossary](GLOSSARY.md) | The deterministic routing layer. **Not** an event bus. |
| event hub | [Glossary](GLOSSARY.md) | The diagnostic ring buffer. **Not** an event bus. |
| plugin fact | [Glossary](GLOSSARY.md) | The append-only durable record. |
| plugin state | [Glossary](GLOSSARY.md) | The compatibility view. Derived, not authoritative. |
| result | [Glossary](GLOSSARY.md) | The summary a plugin returns. |
| skill | [Glossary](GLOSSARY.md) | A machine-readable capability description. |

### 3c. Vault and attestation

| Technical Name | Source | Note |
|---|---|---|
| vault | [Glossary](GLOSSARY.md) | The age-encrypted secret store. |
| principal | [Glossary](GLOSSARY.md) | A deliver-to identity. Not an account. Not a worker. |
| secret | [Secrets](SECRETS.md) | A vault-held value with a lifecycle. |
| secret zero | [Deployment Postures](DEPLOYMENT_POSTURES.md) | The age private key. |
| age key | [Secrets](SECRETS.md) | The file that holds secret zero. |
| nonce | [Glossary](GLOSSARY.md) | The 32-byte value that keys the fingerprints. |
| fingerprint | [Glossary](GLOSSARY.md) | The keyed hash of a plugin's bytes. |
| attestation | [Glossary](GLOSSARY.md) | The binding of a plugin to its bytes. |
| plugin lock | [Glossary](GLOSSARY.md) | The act that records a fingerprint. |
| config lock | [Glossary](GLOSSARY.md) | The act that re-hashes the config files. |
| genesis | [Bootstrap](BOOTSTRAP.md) | The one-time vault creation. |
| admin token | [Bootstrap](BOOTSTRAP.md) | The credential that operates `/vault/*`. |

### 3d. Privilege separation

| Technical Name | Source | Note |
|---|---|---|
| posture | [Deployment Postures](DEPLOYMENT_POSTURES.md) | A deployment-wide choice. |
| tier | [Deployment Postures](DEPLOYMENT_POSTURES.md) | A per-plugin choice. Not a posture. |
| account | [Deployment Postures](DEPLOYMENT_POSTURES.md) | An OS user a plugin drops to. Not a worker. |
| state directory | [Deployment](DEPLOYMENT.md) | The `0700` directory an account owns. |
| cap-only | [Deployment Postures](DEPLOYMENT_POSTURES.md) | The gateway holds `CAP_SETUID` and `CAP_SETGID` only. |
| confined | [Deployment Postures](DEPLOYMENT_POSTURES.md) | The walled account mode. |
| credentialed | [Deployment Postures](DEPLOYMENT_POSTURES.md) | The account mode that uses a real home. |
| unconfined | [Deployment Postures](DEPLOYMENT_POSTURES.md) | The no-drop state. Never an account name. |
| boot gate | [Deployment](DEPLOYMENT.md) | The capability and accounts agreement check. |
| admission gate | [Config Reference](CONFIG_REFERENCE.md) | One `service.admission` control. |
| side-door | [Deployment Postures](DEPLOYMENT_POSTURES.md) | A route from an account to root. |
| wall-bite | [Deployment](DEPLOYMENT.md) | The test that proves the wall denies access. |
| management posture | [Bootstrap](BOOTSTRAP.md) | Vault endpoints on the socket only. |
| gateway posture | [Bootstrap](BOOTSTRAP.md) | The full API is active. |

---

## 4. Technical Verbs

Use these verbs for these actions. The CLI action names in
[CLI Design](CLI_DESIGN_PRINCIPLES.md) §2.2 and these verbs are the same words on purpose: the
sentence that describes the action uses the verb that performs it.

| Technical Verb | Meaning in ductile | Do not use |
|---|---|---|
| check | Validate syntax, policy, and integrity. | validate, verify, test |
| lock | Authorize the current bytes by recording a hash. | seal, bless, approve, sign |
| attest | Bind a plugin to its exact bytes. | certify, trust |
| get | Read one resolved value. | fetch, retrieve, read |
| set | Write one value, or mint one secret. | put, update, change |
| show | Display a resolved entity or config node. | print, dump, cat |
| inspect | Examine one runtime instance in depth. | debug, examine, drill |
| list | Show a summary of resources. | enumerate, ls |
| run | Start one plugin command now. | execute, invoke, fire, call |
| trigger | Start a job from an event or a retry. | kick, fire, launch |
| purge | Delete a resource and its contents. | nuke, wipe, blow away, clear |
| start | Put the gateway into service. | boot, bring up, spin up, launch |
| stop | Take the gateway out of service. | kill, tear down, halt |
| reload | Re-read the config in a running gateway. | refresh, restart |
| enqueue | Append a job to the queue transactionally. | submit, push, post |
| dequeue | Take the next eligible job from the queue. | pull, pop, consume |
| spawn | Create the plugin subprocess. | fork, exec, shell out |
| drop | Change the process to an account uid and gid. | switch, sudo to, become |
| deliver | Give a principal's secrets to a plugin at spawn. | inject, pass, hand |
| compose | Resolve which secrets a principal receives. | build, assemble, gather |
| register | Add a principal to the vault. | create, add, enrol |
| grant | Authorize a principal to receive a secret. | allow, permit, give |
| revoke | Withdraw a grant. | remove, cancel, disable |
| roll | Replace a secret value and keep its name. | rotate, cycle, refresh |
| mint | Create a new credential value. | generate, issue, make |
| refuse | Stop at a gate and do not start. | reject, abort, die, bail |
| route | Map an event to a downstream action. | dispatch, forward, send |
| emit | Produce an event or a fact. | raise, fire, publish |

!!! note "`refuse` is reserved for a gate"
    A gate **refuses**. A job **fails**. A command **returns a non-zero exit code**. Do not mix
    these three.

---

## 5. Do not use these words

One meaning per word. The left column is not wrong English; it is a synonym the project has already
spent a word on.

| Do not write | Write | Reason |
|---|---|---|
| event bus | event router, or event hub | [Glossary](GLOSSARY.md) and [Architecture](ARCHITECTURE.md) both state ductile has no event bus. Name the one you mean. |
| seal | lock | `lock` is the command. `seal` appears as a leftover in several pages. |
| daemon, service, server | gateway | One process, one name. `service` refers only to the systemd unit. |
| user | account, or operator | `account` is the privsep OS user. `operator` is the human. |
| worker | account, principal, or worker | Three unrelated things. Use the one you mean. |
| token, key, credential | admin token, API token, age key, secret | Each has a distinct lifecycle. |
| lock down, harden | confine, or enable the admission gates | Name the mechanism. |
| just, simply, obviously | *(delete)* | These words carry no information and misstate difficulty. |
| should | must, or will | State the obligation or the behaviour. |
| e.g., i.e. | for example, that is | Spell it out. |
| blow away, nuke, wipe | purge, or remove | |
| spin up, bring up, fire up | start | |
| kill | stop | Reserve `kill` for the signal. |

---

## 6. Add a word

Do not invent a synonym for an approved word. If a new thing needs a name:

1. Confirm no approved name covers it. Search this page and [Glossary](GLOSSARY.md).
2. Add the meaning to [Glossary](GLOSSARY.md), or to the ADR that introduces the thing.
3. Add the word to §3 or §4 of this page, with the source page.
4. If the new word replaces a word already in use, add the old word to §5.

A new word in the code and no entry here is an incomplete change. See
[CONTRIBUTING.md](https://github.com/mattjoyce/ductile/blob/main/CONTRIBUTING.md).

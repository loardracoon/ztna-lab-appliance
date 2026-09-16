# Sophos firewall policy optimization (`ztna-lab fwopt`)

`fwopt` reads the IPv4 rule base of a Sophos Firewall over the Firewall
Configuration REST API, reports the rules that are dead, duplicated,
over-broad or invisible to the log, and — only when told to — applies the
fixes back through the same API.

It is read-only unless `-apply` is given, and even then it only performs the
operations named in `-allow`.

---

## Contents

- [What it talks to](#what-it-talks-to)
- [Getting an API key](#getting-an-api-key)
- [Usage](#usage)
- [The checks](#the-checks)
- [How coverage is decided](#how-coverage-is-decided)
- [Applying changes](#applying-changes)
- [Exit codes](#exit-codes)
- [Working offline](#working-offline)
- [Package layout](#package-layout)

---

## What it talks to

| | |
|---|---|
| Base URL | `https://<host>:<port>/api/firewall-config/v1` |
| Auth | `Authorization: Bearer <api-key>` |
| Endpoints used | `GET /firewall/rules/ipv4`, `PATCH /firewall/rules/ipv4/{idOrName}`, `DELETE /firewall/rules/ipv4/{idOrName}`, `POST /firewall/rules/ipv4/move` |

`<port>` is the WebAdmin port, `4444` by default — the same host and port
spelled in the API's own server block (`172.16.16.16:4444`).

The client also implements `POST /firewall/rules/ipv4` (create) and
`GET /firewall/rules/ipv4/{idOrName}`, which the optimizer itself does not
need but which are there for anything built on the package.

Reference: <https://docs.sophos.com/nsg/sophos-firewall/rest-api/>

### Rule order is the whole game

The list endpoint returns rules in the order the firewall evaluates them, and
nothing in a rule object records its position. The slice index *is* the
position, so `fwopt` never re-sorts what it fetches. Everything it says about
shadowing and redundancy follows from that order.

---

## Getting an API key

On the firewall:

1. **Administration → Device access** (or **Backup & firmware → API** on older
   builds) — allow API access from the network you will run this from.
2. Create or pick an administrator profile with rights over firewall rules.
3. Generate an API key for that account. The key inherits exactly that
   account's permissions, so a read-only profile gives you a review-only key —
   which is a good way to run this the first time.

```bash
export SOPHOS_FW_HOST=10.0.0.1:4444
export SOPHOS_FW_API_KEY='…'
```

Sophos appliances ship with a self-signed WebAdmin certificate, so most labs
need `-insecure` (or `SOPHOS_FW_INSECURE=true`). Drop it wherever the
appliance has a certificate your machine trusts.

---

## Usage

```bash
# Review a firewall. Nothing is written.
ztna-lab fwopt -host 10.0.0.1:4444 -api-key "$KEY" -insecure

# Keep a copy of the rule base, then work against the file.
ztna-lab fwopt -host 10.0.0.1:4444 -api-key "$KEY" -save rules.json
ztna-lab fwopt -in rules.json -format json -out review.json

# Only the serious findings.
ztna-lab fwopt -host 10.0.0.1:4444 -api-key "$KEY" -min-severity medium

# Apply just the reversible fixes.
ztna-lab fwopt -host 10.0.0.1:4444 -api-key "$KEY" -apply -allow log,merge

# Clean out dead rules without deleting anything.
ztna-lab fwopt -host 10.0.0.1:4444 -api-key "$KEY" \
    -apply -allow disable -prefer-disable
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-host` | `$SOPHOS_FW_HOST` | Firewall address and admin port, e.g. `10.0.0.1:4444`. A full URL also works. |
| `-api-key` | `$SOPHOS_FW_API_KEY` | API key, sent as a bearer token. |
| `-insecure` | `$SOPHOS_FW_INSECURE` | Skip TLS verification. |
| `-timeout` | `30s` | Per-request timeout. |
| `-in` | — | Analyze a saved rule dump instead of calling the firewall. |
| `-save` | — | Write the fetched rules to this file. |
| `-format` | `text` | `text` or `json`. |
| `-out` | stdout | Write the report here. |
| `-min-severity` | `info` | Drop findings below `info`, `low`, `medium` or `high`. A dropped finding also drops out of the plan, so raising this narrows what `-apply` will do. |
| `-skip` | — | Comma-separated checks to leave out, e.g. `no-logging,disabled`. |
| `-fail-on` | — | Exit 3 if any finding reaches this level. For pipelines. |
| `-apply` | `false` | Send the plan to the firewall. |
| `-allow` | `log` | Operations the plan may use: `log`, `disable`, `move`, `merge`, `delete` — or `all` / `none`. |
| `-prefer-disable` | `false` | Turn every delete into a disable. |
| `-backup` | `./sophos-rules-<timestamp>.json` | Where to write the pre-change backup. |
| `-yes` | `false` | Skip the confirmation prompt. |

---

## The checks

| Check | Severity | What it means | Offered fix |
|---|---|---|---|
| `shadowed` | high | An earlier rule matches everything this one matches and does something **different** with it. The rule never evaluates, so the policy it expresses is not in force. | move it above the rule hiding it |
| `permissive` | high | An `accept` rule with any source, any destination and any service. | — |
| `redundant` | medium | An earlier rule matches everything this one matches and treats it **the same way**. The rule never evaluates and removing it changes nothing. | delete |
| `duplicate` | medium | Same as redundant, and the two rules match *exactly* the same traffic. | delete |
| `mergeable` | low | Two adjacent rules with the same treatment that differ in exactly one dimension. | merge |
| `broad-service` | low | An `accept` rule that constrains source and destination but allows every service between them. | — |
| `no-inspection` | low | An `accept` rule with no IPS policy, web policy, application policy or scanning at all. | — |
| `no-logging` | low | `logTraffic` is off, so nothing the rule matches reaches the log. | enable logging |
| `disabled` | info | A switched-off rule still sitting in the rule base. | delete |

A rule the analyzer says is dead does not also collect hygiene findings — no
point telling anyone to switch logging on for a rule that never matches.

---

## How coverage is decided

Shadowing, redundancy and duplication all rest on one question: does rule A
match everything rule B matches?

The API references networks, services and zones **by name**, never by address.
So the analyzer compares sets of names, not sets of addresses. That direction
is safe: two rules naming the same object match the same traffic, so a
name-set superset is always a real traffic superset.

It is deliberately **not** complete. Two differently named address objects
holding the same subnet will not be spotted, and names are compared verbatim —
`LAN` and `lan` are treated as different objects. Missing a finding costs an
admin nothing; inventing one costs them a live rule.

A rule is only ever claimed to cover another when every one of these holds:

- both are `firewall`-type rules (WAF rules are left alone);
- the covering rule is enabled — a disabled rule shadows nothing;
- the covering rule has no **exclusions**, which would punch holes the
  analyzer cannot measure;
- the covering rule has no **schedule**, or the same one as the covered rule;
- the covering rule has no **user restriction**, or the same one;
- the covering rule has no **Security Heartbeat requirement**, or the same one;
- both rules carry all five match dimensions. A rule missing one is skipped
  rather than guessed at.

Only the *first* covering rule is reported for each dead rule — that is the one
actually eating the traffic. Coverage by the union of several earlier rules is
not detected.

**Redundant vs shadowed** turns on whether the two rules do the same thing:
same action, and for `accept` rules the same security features, mail scanning,
QoS and heartbeat settings. Same treatment means the covered rule can simply
go. Different treatment means somebody wrote it for a reason that is not
taking effect, which is the more serious finding.

Logging is not part of that comparison. A covered rule never matches a packet,
so it never logs either — removing it loses nothing that was happening.

**Merges** are stricter still. Two rules are only mergeable when they are
adjacent (nothing evaluates between them), carry no exclusions, schedules,
user matches or heartbeat requirements, agree on action, inspection *and*
logging, and differ in exactly one dimension where neither side covers the
other. Under those conditions the union of the two rules, placed at the first
one's position, matches exactly what the pair matched.

---

## Applying changes

`-apply` sends the plan. Before anything is written:

1. The plan is printed to stderr, so it can be read first — whatever format
   the report itself is in.
2. A confirmation is required — anything but a typed `yes` aborts, including
   end of input. Unattended runs pass `-yes`.
3. The full rule base is written to the backup file. If the backup cannot be
   written, nothing is applied.

The report goes to stdout (or `-out`) once, at the end, with the plan and what
came of each action included. Everything interactive stays on stderr, so
`-format json` piped somewhere stays a single valid document.

Where a finding offers more than one fix they are **alternatives**, not a
sequence — the planner takes the first one you allowed and never both. That is
what `-prefer-disable` rides on: it rewrites a deletion into a disable, which
then still has to clear `-allow`.

**A shadowed rule is never deleted automatically.** Its only offered fix is the
promotion, even under `-allow all`. A shadowed rule does something the rule
above it does not; deleting it would erase a policy somebody wrote on purpose
along with the evidence that it is not working. Redundant, duplicate and
disabled rules are the ones this tool removes. If a shadowed rule really is
obsolete, that is a judgement about intent, and it belongs to the admin.

The plan is then made conflict-free and ordered:

- no rule is targeted twice;
- a move whose anchor rule is being removed is dropped;
- logging fixes run first, then merges, then moves, then disables and deletes;
- removals run bottom-up, so positions stay stable.

Execution stops at the first failure. The rule base is then partly changed, and
the error says which backup file to restore from.

### Operations

| `-allow` | API call | Changes what the firewall forwards? |
|---|---|---|
| `log` | `PATCH {"logTraffic": true}` | no |
| `disable` | `PATCH {"enabled": false}` | only for rules that were live |
| `merge` | `PATCH` the survivor with the union, then `DELETE` the other | no |
| `move` | `POST /move` with `before` and an anchor | **yes** |
| `delete` | `DELETE` | no — only offered on redundant, duplicate and disabled rules, which are dead already |

The default is `-allow log`: the one fix that cannot change what crosses the
firewall.

### A safe first pass

```bash
# 1. Look.
ztna-lab fwopt -host "$FW" -api-key "$KEY" -save before.json

# 2. Turn on logging everywhere, so the next pass has evidence behind it.
ztna-lab fwopt -host "$FW" -api-key "$KEY" -apply -allow log

# 3. Weeks later, retire the dead rules by disabling them first.
ztna-lab fwopt -host "$FW" -api-key "$KEY" -apply -allow disable -prefer-disable

# 4. Once nothing has broken, delete them for real.
ztna-lab fwopt -host "$FW" -api-key "$KEY" -apply -allow delete
```

### What it cannot know

There are no hit counters in this API, so `fwopt` cannot tell you which rules
are unused in practice, and it never reorders rules for performance. Every
ordering change it proposes comes from a shadowing finding, where the current
order provably defeats a rule.

---

## Exit codes

| Code | Meaning |
|---|---|
| `0` | The run finished. |
| `1` | The tool could not do its job — bad flags, no connection, an API error, an aborted confirmation. |
| `3` | The run finished and findings reached `-fail-on`. |

```bash
# Fail a pipeline on a shadowed or wide-open rule.
ztna-lab fwopt -host "$FW" -api-key "$KEY" -fail-on high -format json -out review.json
```

---

## Working offline

`-save` writes the fetched rules; `-in` reads them back. The file is a plain
JSON array of rules, and the API's `{"items": […]}` envelope is accepted too,
so a `curl` of the list endpoint can be fed straight in:

```bash
curl -sk -H "Authorization: Bearer $KEY" \
  'https://10.0.0.1:4444/api/firewall-config/v1/firewall/rules/ipv4?pageSize=200' \
  > rules.json

ztna-lab fwopt -in rules.json
```

`-apply` is refused with `-in`: the file may no longer match the firewall.

Backups are written with mode `0600` — they are a complete copy of the
firewall's policy.

---

## Package layout

```
sophos/
├── types.go       IPv4 rule model, mirroring the API schema
├── client.go      REST client: list (paginated), get, create, update, delete, move
├── coverage.go    Set semantics — the "does A cover B?" question and its guards
├── analyzer.go    The checks, and the findings they produce
├── plan.go        Findings → API calls, conflict resolution, apply, save/load
└── report.go      Text and JSON rendering

fwopt.go           The `ztna-lab fwopt` subcommand: flags, safety rails, output
```

The package has no dependency on the rest of the appliance and can be used on
its own:

```go
c, err := sophos.NewClient(sophos.Config{Host: "10.0.0.1:4444", APIKey: key, Insecure: true})
rules, err := c.ListIPv4Rules(ctx)

rep := sophos.Analyze(rules, sophos.Options{MinSeverity: sophos.SeverityMedium})
plan := sophos.BuildPlan(rep, sophos.PlanOptions{Allow: map[sophos.Op]bool{sophos.OpEnableLogging: true}})

results, err := plan.Apply(ctx, c, true) // dry run
```

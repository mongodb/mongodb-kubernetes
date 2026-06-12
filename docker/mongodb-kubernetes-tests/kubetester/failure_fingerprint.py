"""Normalize e2e failure messages into low-cardinality fingerprints and categories.

`assert_reaches_phase` (and friends) raise free-text exceptions whose only variable
parts are resource names, timeouts, org ids, process lists, etc. Breaking failures down
by the raw message yields ~1000 distinct strings; masking those variable tokens collapses
them to a few hundred templates where the top ~30 cover ~94% of failures. Emitting the
fingerprint and a coarse category as span attributes lets Honeycomb group failures without
regex-on-message in every query.

The masking is a fixed, ordered substitution list (Drain/Datadog-Patterns style, but
precomputed rather than learned) so it stays deterministic and reviewable.
"""

import re

# Ordered masking substitutions. Order matters: most-specific first.
# Rule of thumb: mask identifiers/counts/names/hosts/urls; KEEP semantically meaningful
# words (error classes, HTTP status codes, phase names) so distinct failures stay distinct.
_SUBS = [
    # collapse repeated "StatefulSet not ready, StatefulSet not ready, ..." -> one
    (re.compile(r"(StatefulSet not ready)(, StatefulSet not ready)+"), r"\1"),
    # process-name lists in brackets, with or without @-1:
    #   [sh001-single-0-0@-1 sh001-single-mongos-0@-1]  AND  [sh001-single-config-0]
    (re.compile(r"\[[a-z0-9][\w.@-]*(?:[ ,]+[\w.@-]+)*\]"), "[<procs>]"),
    (
        re.compile(r"\d+ processes waiting to reach automation config goal state \(version=\d+\)"),
        "<n> processes waiting to reach goal state (version=<n>)",
    ),
    (re.compile(r"\d+ processes reached goal state"), "<n> processes reached goal state"),
    (re.compile(r"Timeout \(\d+\)"), "Timeout (<n>)"),
    (re.compile(r"MongoDB \([^)]*\)"), "MongoDB (<name>)"),
    (re.compile(r"organization with id [0-9a-f]{24}"), "organization with id <id>"),
    (re.compile(r"id [0-9a-f]{24}"), "id <id>"),
    (re.compile(r'ConfigMap "[^"]*"'), 'ConfigMap "<name>"'),
    (re.compile(r'configmaps "[^"]*"'), 'configmaps "<name>"'),
    (re.compile(r"project a-\d+-\w+"), "project <name>"),
    (re.compile(r"a-\d+-\w+"), "<ns>"),
    (re.compile(r'https?://[^\s"]+'), "<url>"),
    (re.compile(r"\d+\.\d+\.\d+\.\d+:\d+"), "<addr>"),
    (re.compile(r"\d+\.\d+\.\d+\.\d+"), "<ip>"),
    (re.compile(r"retrying in \d+ seconds"), "retrying in <n> seconds"),
    (re.compile(r"Replica Set _id: [\w-]+"), "Replica Set _id: <name>"),
    (re.compile(r"finding project [\w.-]+"), "finding project <name>"),
    (re.compile(r"giving up after \d+ attempt"), "giving up after <n> attempt"),
    (re.compile(r"hosts \[[^\]]*\]"), "hosts [<hosts>]"),
    (re.compile(r"kind-e2e-cluster-\d+"), "kind-e2e-cluster-<n>"),
    (re.compile(r"service: [\w.-]+"), "service: <name>"),
    (re.compile(r"statefulset in member cluster [\w.-]+"), "statefulset in member cluster <name>"),
    (re.compile(r"version [\d.]+-ent"), "version <ver>-ent"),
]


def failure_fingerprint(message: str | None) -> str:
    """Return a low-cardinality template for a failure message by masking variable tokens.

    Idempotent: fingerprinting an already-fingerprinted string returns it unchanged.
    """
    if not message:
        return ""
    s = message
    for pattern, repl in _SUBS:
        s = pattern.sub(repl, s)
    return s


# Category classification works on the *fingerprint* (a few hundred stable templates),
# not the raw message. An unmatched fingerprint falls to "unknown" - and because each
# unmatched fingerprint is itself a distinct, countable value, a growing "unknown" is a
# visible signal to add a rule, never a silent catch-all.
#
# Substrings are checked against the fingerprint in priority order.
_CATEGORY_RULES = [
    # deterministic bad-spec failures - NOT flakes; keep separable so they don't pollute flake rate
    ("spec_invalid", ("Bad Request", "Invalid config", "Required value", "Cannot have more than 1 MongoDB Cluster", "must not end with")),
    # operator/agent registration stall
    ("agents_not_ready", ("reach automation config goal state", "haven't reached READY state", "reached goal state")),
    # transient infrastructure / Cloud-QA / etcd / network
    ("infra", ("401", "Unauthorized", "etcdserver", "Conflict", "connection refused", "read: connection reset", "unexpected EOF", '": EOF', "Client.Timeout", "context deadline exceeded")),
    # pods/STS/RS never became ready
    ("not_ready", ("StatefulSet not ready", "ReplicaSet is not yet ready", "deployment is not yet ready")),
]


def failure_category(fingerprint: str) -> str:
    """Map a fingerprint to a coarse category for alerting / flake-rate filtering.

    Returns one of: spec_invalid, agents_not_ready, infra, not_ready, unknown.
    """
    if not fingerprint:
        return "unknown"
    for category, needles in _CATEGORY_RULES:
        if any(n in fingerprint for n in needles):
            return category
    return "unknown"

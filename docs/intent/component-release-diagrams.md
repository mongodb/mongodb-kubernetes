# Intent: component release process diagrams (min/latest hybrid)

Status: confirmed in conversation, 2026-10-26. Illustrative numbers: the proxy has not shipped.

## Outcome
Two archify workflow diagrams, each following one PR, left to right: CI gate, hook computation, committed diff, customer outcome.

1. **PR without a component entry.** Last release 1.14.0, `proxyMinimumVersion` 1.12.0. The PR is an operator-only fix. The component changelog check and depguard pass. The hook computes next MCK 1.14.1, `proxyLatestVersion` 1.14.1, and the minimum stays 1.12.0. The diff covers `release.json`, Helm values, OLM values (both versions in `relatedImages`) and the yaml.
2. **PR with a component entry.** The PR changes the proxy code and adds `changelog/proxy/...`. The check requires the entry (a failing branch is shown when it is missing), and depguard blocks imports of other MCK packages. The hook computes next MCK 1.14.1 and the minimum becomes 1.14.1.

## Release
A single closing box: "publishes all components at 1.14.1". All files are already committed in the PR.

## Payoff (end of each diagram)
- Without an entry: Helm and OLM on minimum get no new tag and no restart. Yaml and "track latest" get 1.14.1 and a restart.
- With an entry: everyone gets 1.14.1 and a rolling restart.

## Naming
`proxyMinimumVersion` and `proxyLatestVersion` (same pair for the reporter). "Pinned" is dropped.

## Audience and purpose
Nam, Lukasz, Igor and the Kubernetes team in a meeting. Goal: agreement that hybrid is the preferred option, not approval of the implementation.

## Format
HTML for the meeting; static images pasted into the TD, so each diagram has a self-explanatory caption with its numbers.

## Out of scope
A release-time process diagram, a per-channel settings matrix (stays a TD table), reporter and opt-in details beyond the outcome lines, edits to the Google Doc.

## Decisions behind the diagrams
- Helm and OLM default to minimum; static yaml to latest (yaml users cannot opt in easily).
- OLM `RELATED_IMAGE_*` lists both minimum and latest per component.
- One global "track latest" switch, resolving to this operator release's own component versions.
- A manual component version is allowed but unsupported.
- Every component changelog entry bumps the minimum, whatever its kind.
- Reachable critical or high dependency vulnerabilities need a fix entry (review rule, needs team agreement).
- depguard: the proxy imports no other MCK package; the reporter may import a named allowlist (the readiness `health` types).
- Smoke gate on master merges: operator HEAD against the minimum component images.

## Revision (2026-10-26): PR only
The diagrams now cover only the PR, and depguard is left out.
- Removed: depguard, the Release box, and the Release-and-customers lane.
- Chain: last release tag, PR, changelog check (with "PR blocked" in the entry diagram), pre-commit hook, `release.json`, then Helm values, OLM values and static yaml, each with its version values.
- The customer outcome (restart yes or no per channel) moved into the caption card under each diagram.

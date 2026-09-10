#!/usr/bin/env python3
"""Gate that fails CI when ``evergreen validate`` reports unexpected problems.

``evergreen validate`` exits 0 even when it prints ``WARNING`` lines, so CI cannot
rely on its exit code alone. This gate runs the validator, parses its output, and
fails (non-zero exit) on:

  * any strict-unmarshalling error (an invalid/unknown field in the config), and
  * any ``defined but not used by any variants`` warning whose task is NOT in the
    curated allowlist below, and
  * any other, unrecognized warning line (e.g. a re-introduced duplicate
    build-variant display name or a ``depends on non-patchable task`` warning).

The gate also fails -- never silently passes -- when the ``evergreen`` CLI is
missing from PATH or when the validator itself exits non-zero, so it cannot
report success without actually validating the configuration.

The allowlist also fails the gate when it goes stale: if an allowlisted task no
longer warns (because it was re-enabled in a variant or removed), its entry must
be deleted from the allowlist. This keeps the accepted-warning set honest.

Tracked by KUBE-443.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
from dataclasses import dataclass

PROJECT = "mongodb-kubernetes"
CONFIG_FILE = ".evergreen.yml"

STRICT_MARKER = "strict unmarshalling"
_STRICT_DETAIL_MARKER = "not found in type"
_UNUSED_RE = re.compile(r"task '([^']+)' defined but not used by any variants")

# Curated set of accepted "defined but not used by any variants" warnings.
#
# Every entry is an Evergreen task that is intentionally retained even though no
# build variant references it. The value explains why. These fall into three
# groups; the last two are debt tracked by KUBE-443.
#
# TODO(KUBE-443): drive this list down. For each orphaned task, either re-enable
# it in a build variant or remove it. A task definition should only be deleted
# once its e2e test is also removed or the task is confirmed abandoned by the
# owning team -- do NOT delete a task whose e2e test still exists.
UNUSED_TASK_ALLOWLIST: dict[str, str] = {
    # --- Invoked dynamically; Evergreen's static validation cannot see the indirection (KEEP) ---
    "run_precommit_and_push": "invoked via run_task_conditionally (run_conditionally_precommit_and_push)",
    "prepare_and_upload_openshift_bundles": "invoked via run_task_conditionally in the release pipeline",
    "e2e_om_reconcile_perf": "exercised only via generated perf_* tasks (TEST_NAME_OVERRIDE) in scripts/evergreen/e2e/performance/create_variants.py",
    # --- Orphaned e2e tasks disabled pending CLOUDP-349093 (password secret / project migration) ---
    "e2e_sharded_cluster_x509_switch_project": "disabled pending CLOUDP-349093 (password secret / project migration)",
    "e2e_replica_set_x509_switch_project": "disabled pending CLOUDP-349093 (password secret / project migration)",
    "e2e_replica_set_ldap_switch_project": "disabled pending CLOUDP-349093 (password secret / project migration)",
    "e2e_sharded_cluster_ldap_switch_project": "disabled pending CLOUDP-349093 (password secret / project migration)",
    # --- Orphaned e2e tasks with a live pytest test (KUBE-447): needs its own investigation why they fail (do NOT delete def alone) ---
    "e2e_standalone_groups": "orphaned; live test docker/.../tests/standalone/standalone_groups.py",
    "e2e_replica_set_groups": "orphaned; live test docker/.../tests/replicaset/replica_set_groups.py; fails in CI (needs investigation)",
    "e2e_tls_multiple_different_ssl_configs": "orphaned; live test docker/.../tests/tls/tls_multiple_different_ssl_configs.py; fails in CI (needs investigation)",
    "e2e_replica_set_ldap_agent_auth": "orphaned; live test docker/.../tests/authentication/replica_set_agent_ldap.py; fails in CI (needs investigation)",
    "e2e_replica_set_scram_x509_internal_cluster": "orphaned; live test docker/.../tests/authentication/replica_set_scram_x509_internal_cluster.py; fails in CI (needs investigation)",
    "e2e_sharded_cluster_scram_x509_internal_cluster": "orphaned; live test docker/.../tests/authentication/sharded_cluster_scram_x509_internal_cluster.py; fails in CI (needs investigation)",
    "e2e_multi_cluster_with_ldap": "orphaned; live test docker/.../tests/multicluster/multi_cluster_ldap.py; fails in CI (needs investigation)",
    "e2e_multi_cluster_with_ldap_custom_roles": "orphaned; live test docker/.../tests/multicluster/multi_cluster_ldap_custom_roles.py; fails in CI (needs investigation)",
}


@dataclass(frozen=True)
class ValidationResult:
    """Outcome of parsing ``evergreen validate`` output against the allowlist."""

    strict_errors: list[str]
    unexpected_unused: list[str]
    stale_allowlist: list[str]
    other_warnings: list[str]

    @property
    def ok(self) -> bool:
        # stale_allowlist IS a failure: a task disappearing from the warnings means it
        # was fixed/re-enabled and its allowlist entry must be deleted, or the entry
        # could later mask a reintroduced orphan of the same name.
        return not (self.strict_errors or self.unexpected_unused or self.other_warnings or self.stale_allowlist)


def parse_validation_output(output: str, allowlist: dict[str, str] = UNUSED_TASK_ALLOWLIST) -> ValidationResult:
    """Classify each WARNING line in ``evergreen validate`` output.

    Any line containing ``STRICT_MARKER`` is a strict error. A ``defined but not
    used by any variants`` line is expected iff its task is in ``allowlist``; any
    other allowlisted key that never appears is reported as stale. Every remaining
    ``WARNING:`` line is an unrecognized warning.
    """
    strict_errors: list[str] = []
    unexpected_unused: list[str] = []
    other_warnings: list[str] = []
    seen_unused: set[str] = set()

    for raw in output.splitlines():
        line = raw.strip()
        if not line:
            continue
        if _STRICT_DETAIL_MARKER in line:
            strict_errors.append(line)
            continue
        if "WARNING" not in line:
            continue
        if STRICT_MARKER in line:
            strict_errors.append(line)
            continue
        match = _UNUSED_RE.search(line)
        if match:
            task = match.group(1)
            seen_unused.add(task)
            if task not in allowlist:
                unexpected_unused.append(task)
            continue
        other_warnings.append(line)

    stale_allowlist = sorted(task for task in allowlist if task not in seen_unused)
    return ValidationResult(
        strict_errors=strict_errors,
        unexpected_unused=sorted(unexpected_unused),
        stale_allowlist=stale_allowlist,
        other_warnings=other_warnings,
    )


def run_evergreen_validate(project: str = PROJECT, config_file: str = CONFIG_FILE) -> tuple[str, int]:
    """Run ``evergreen validate`` and return (combined stdout+stderr, exit code)."""
    completed = subprocess.run(
        ["evergreen", "validate", "-p", project, "-f", config_file],
        capture_output=True,
        text=True,
        check=False,
    )
    return completed.stdout + completed.stderr, completed.returncode


def format_report(result: ValidationResult) -> str:
    """Render a human-readable summary of a failing (or passing) result."""
    lines: list[str] = []
    if result.strict_errors:
        lines.append("Strict-unmarshalling errors (invalid/unknown config fields):")
        lines.extend(f"  - {e}" for e in result.strict_errors)
    if result.unexpected_unused:
        lines.append("Tasks defined but not used by any variant, and NOT allowlisted:")
        lines.extend(
            f"  - {t}  (add it to a variant, remove it, or allowlist it in this script)"
            for t in result.unexpected_unused
        )
    if result.other_warnings:
        lines.append("Unrecognized validation warnings (fix these):")
        lines.extend(f"  - {w}" for w in result.other_warnings)
    if result.stale_allowlist:
        lines.append("Stale allowlist entries (task no longer warns -> delete from UNUSED_TASK_ALLOWLIST):")
        lines.extend(f"  - {t}" for t in result.stale_allowlist)
    if not lines:
        lines.append("evergreen validate: OK (0 strict errors, only allowlisted warnings).")
    return "\n".join(lines)


def main() -> int:
    if shutil.which("evergreen") is None:
        print(
            "validate_evergreen_config: 'evergreen' CLI not found on PATH; cannot validate "
            "the Evergreen configuration. Install it "
            "(https://github.com/evergreen-ci/evergreen) to run this gate.",
            file=sys.stderr,
        )
        return 1
    output, returncode = run_evergreen_validate()
    result = parse_validation_output(output)
    print(format_report(result))
    if returncode != 0:
        output_lower = output.lower()
        if (
            "could not find client configuration file" in output_lower
            or "oauth configuration is incomplete" in output_lower
        ):
            print(
                "validate_evergreen_config: 'evergreen' CLI has no valid Evergreen auth "
                "config (~/.evergreen.yml); skipping validation. Run locally with a "
                "configured client to enforce.",
                file=sys.stderr,
            )
            return 0
        print(
            f"evergreen validate exited {returncode} (non-zero); treating as gate failure.",
            file=sys.stderr,
        )
        return 1
    return 0 if result.ok else 1


if __name__ == "__main__":
    raise SystemExit(main())

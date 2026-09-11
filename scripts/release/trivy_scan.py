#!/usr/bin/env python3
"""
CVE scanning for freshly built and published container images.

Scans images produced by the build-and-publish pipeline (scripts/release/pipeline.py)
with trivy and notifies Slack (#k8s-enterprise-builds) when CRITICAL/HIGH
vulnerabilities are detected, so the team can triage quickly and answer whether
an issue is fixed (a patched package version exists) or not fixed upstream.

The image repositories and platforms are resolved from 'build_info.json' for the
given build scenario - exactly like the build pipeline does - so the scan always
targets the image coordinates that were just pushed.

Environment variables:
    SLACK_WEBHOOK_URL: Slack webhook used for notifications (optional; stdout only if unset)
    TRIVY_BIN: path to the trivy binary (default: ${PROJECT_DIR}/bin/trivy or trivy from PATH)
    TRIVY_SCAN_NOTIFY: "true"/"false" to force enable/disable Slack notifications.
        By default notifications are only sent for mainline runs (requester is
        "commit", "github_tag" or "trigger") to avoid noise from manual patches.
    requester: Evergreen requester type, used for the notification gating above
    TASK_ID: Evergreen task id, used to link the scan logs in the Slack message

Usage:
    python scripts/release/trivy_scan.py operator --build-scenario staging --version <sha>
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
from dataclasses import dataclass, field
from typing import Optional

import requests

from lib.base_logger import logger
from scripts.release.agent.agents_to_rebuild import get_currently_used_agents
from scripts.release.argparse_utils import get_scenario_from_arg
from scripts.release.build.build_info import AGENT_IMAGE, load_build_info
from scripts.release.build.build_scenario import SUPPORTED_SCENARIOS, BuildScenario

DEFAULT_SEVERITIES = "CRITICAL,HIGH"
DEFAULT_OUTPUT_DIR = "trivy-reports"
TRIVY_TIMEOUT = "15m"
SLACK_MAX_FINDINGS = 10

# Agent builds use the special versions "all"/"current" instead of a single version
AGENT_ALL = "all"
AGENT_CURRENT = "current"

# Requester values for which Slack notifications are enabled by default:
# merges to master ("commit"), git tag releases ("github_tag") and downstream
# triggers. Manual patches and PRs only report to stdout. Note that the new
# release process (release_publish variant) runs as a decider-created patch,
# so should_notify() additionally checks the triggered_by_git_tag expansion.
NOTIFY_REQUESTERS = {"commit", "github_tag", "trigger"}


@dataclass
class Vulnerability:
    id: str
    severity: str
    pkg_name: str
    installed_version: str
    fixed_version: str = ""
    title: str = ""
    primary_url: str = ""

    @property
    def is_fixed(self) -> bool:
        """True when a patched package version exists (the CVE is fixed upstream)."""
        return bool(self.fixed_version)


@dataclass
class ScanResult:
    image_ref: str
    platform: str
    vulnerabilities: list[Vulnerability] = field(default_factory=list)

    def count(self, severity: str, fixed: Optional[bool] = None) -> int:
        return sum(1 for v in self.vulnerabilities if v.severity == severity and (fixed is None or v.is_fixed == fixed))

    def severities(self) -> list[str]:
        seen = []
        for v in self.vulnerabilities:
            if v.severity not in seen:
                seen.append(v.severity)
        return seen


def get_trivy_binary() -> str:
    trivy_bin = os.environ.get("TRIVY_BIN")
    if trivy_bin:
        return trivy_bin

    project_bin_trivy = os.path.join(os.environ.get("PROJECT_DIR", "."), "bin", "trivy")
    if os.path.isfile(project_bin_trivy) and os.access(project_bin_trivy, os.X_OK):
        return project_bin_trivy

    trivy_from_path = shutil.which("trivy")
    if trivy_from_path:
        return trivy_from_path

    raise RuntimeError("trivy binary not found. Run scripts/evergreen/setup_trivy.sh or set TRIVY_BIN.")


def resolve_scan_targets(image: str, scenario: BuildScenario, version: str) -> list[tuple[str, list[str]]]:
    """Resolve the (image_ref, platforms) tuples to scan from build_info.json.

    Mirrors how scripts/release/pipeline.py resolves the pushed image coordinates.
    For the agent image the special versions "all"/"current" are expanded to the
    currently used agent versions to keep scan time bounded.
    """
    build_info = load_build_info(scenario)
    image_info = build_info.images.get(image)
    if not image_info:
        raise ValueError(f"Image '{image}' is not defined in the build info for scenario '{scenario}'")

    versions = [version]
    if image == AGENT_IMAGE and version in (AGENT_ALL, AGENT_CURRENT):
        agents = sorted({agent_version for agent_version, _ in get_currently_used_agents()})
        if not agents:
            raise ValueError("Could not resolve any currently used agent versions to scan")
        logger.info(f"Scanning currently used agent versions instead of '{version}': {agents}")
        versions = agents

    return [(f"{image_info.repository}:{v}", image_info.platforms) for v in versions]


def run_trivy_scan(trivy_bin: str, image_ref: str, platform: str, severities: str, output_file: str) -> ScanResult:
    """Run 'trivy image' against a single image reference/platform and parse the report."""
    command = [
        trivy_bin,
        "image",
        "--platform",
        platform,
        "--scanners",
        "vuln",
        "--severity",
        severities,
        "--format",
        "json",
        "--output",
        output_file,
        "--timeout",
        TRIVY_TIMEOUT,
        "--quiet",
        image_ref,
    ]

    logger.info(f"Scanning {image_ref} ({platform})")
    logger.debug(f"Running: {' '.join(command)}")
    result = subprocess.run(command, capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(
            f"trivy scan failed for {image_ref} ({platform}) with exit code {result.returncode}: {result.stderr.strip()}"
        )

    with open(output_file, "r") as f:
        report = json.load(f)

    return parse_trivy_report(report, image_ref, platform)


def parse_trivy_report(report: dict, image_ref: str, platform: str) -> ScanResult:
    """Convert a trivy JSON report into a ScanResult, deduplicating findings."""
    vulnerabilities = []
    seen = set()
    for result in report.get("Results") or []:
        for vulnerability in result.get("Vulnerabilities") or []:
            key = (
                vulnerability.get("VulnerabilityID", ""),
                vulnerability.get("PkgName", ""),
                vulnerability.get("InstalledVersion", ""),
            )
            if key in seen:
                continue
            seen.add(key)
            vulnerabilities.append(
                Vulnerability(
                    id=vulnerability.get("VulnerabilityID", "unknown"),
                    severity=vulnerability.get("Severity", "UNKNOWN"),
                    pkg_name=vulnerability.get("PkgName", "unknown"),
                    installed_version=vulnerability.get("InstalledVersion", ""),
                    fixed_version=vulnerability.get("FixedVersion", ""),
                    title=vulnerability.get("Title", ""),
                    primary_url=vulnerability.get("PrimaryURL", ""),
                )
            )

    return ScanResult(image_ref=image_ref, platform=platform, vulnerabilities=vulnerabilities)


def severity_order(severity: str) -> int:
    order = {"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "UNKNOWN": 4}
    return order.get(severity, 5)


def summarize_result(result: ScanResult) -> str:
    """One line summary such as 'CRITICAL: 2 (1 fixable), HIGH: 5 (4 fixable)'."""
    if not result.vulnerabilities:
        return "no vulnerabilities found"

    parts = []
    for severity in sorted(result.severities(), key=severity_order):
        total = result.count(severity)
        fixable = result.count(severity, fixed=True)
        parts.append(f"{severity}: {total} ({fixable} fixable)")
    return ", ".join(parts)


def print_stdout_report(results: list[ScanResult]) -> None:
    for result in results:
        print(f"\n=== {result.image_ref} ({result.platform}): {summarize_result(result)} ===")
        if not result.vulnerabilities:
            continue
        print(f"{'ID':<20} {'SEVERITY':<10} {'PACKAGE':<30} {'INSTALLED':<25} FIX STATUS")
        for v in sorted(result.vulnerabilities, key=lambda v: (severity_order(v.severity), v.id)):
            fix_status = f"fixed in {v.fixed_version}" if v.is_fixed else "no fix available"
            print(f"{v.id:<20} {v.severity:<10} {v.pkg_name:<30} {v.installed_version:<25} {fix_status}")


def format_slack_message(image: str, scenario: BuildScenario, results: list[ScanResult]) -> dict:
    """Format the scan findings as a Slack Block Kit message.

    Every finding states whether a fix exists, so the team can quickly answer
    whether an issue is fixed or not fixed.
    """
    total_vulnerabilities = sum(len(r.vulnerabilities) for r in results)

    blocks = [
        {
            "type": "header",
            "text": {"type": "plain_text", "text": f"CVE scan: vulnerabilities found in {image} image"},
        },
        {
            "type": "section",
            "text": {
                "type": "mrkdwn",
                "text": f"Trivy detected *{total_vulnerabilities}* vulnerabilities in newly built `{image}` "
                f"images (build scenario: `{scenario}`):",
            },
        },
        {"type": "divider"},
    ]

    for result in results:
        if not result.vulnerabilities:
            continue

        top_findings = sorted(result.vulnerabilities, key=lambda v: (severity_order(v.severity), v.id))
        lines = []
        for v in top_findings[:SLACK_MAX_FINDINGS]:
            cve = f"<{v.primary_url}|{v.id}>" if v.primary_url else v.id
            fix_status = f"fixed in `{v.fixed_version}`" if v.is_fixed else "*no fix available*"
            lines.append(f"• {cve} [{v.severity}] `{v.pkg_name} {v.installed_version}` — {fix_status}")
        if len(top_findings) > SLACK_MAX_FINDINGS:
            lines.append(f"… and {len(top_findings) - SLACK_MAX_FINDINGS} more (see task logs)")

        blocks.append(
            {
                "type": "section",
                "text": {
                    "type": "mrkdwn",
                    "text": f"*`{result.image_ref}`* ({result.platform})\n"
                    f"{summarize_result(result)}\n" + "\n".join(lines),
                },
            }
        )

    task_id = os.environ.get("TASK_ID", "")
    if task_id:
        blocks.append(
            {
                "type": "context",
                "elements": [
                    {
                        "type": "mrkdwn",
                        "text": f"<https://spruce.mongodb.com/task/{task_id}|View scan logs in Evergreen>",
                    }
                ],
            }
        )

    return {"blocks": blocks}


def should_notify() -> bool:
    """Slack notifications are sent for mainline builds and releases only, unless overridden.

    Releases created through the release decider run as Evergreen patches
    (requester "patch") but carry the triggered_by_git_tag expansion, so a
    non-empty git tag also enables notifications.
    """
    override = os.environ.get("TRIVY_SCAN_NOTIFY", "").lower()
    if override in ("true", "false"):
        return override == "true"
    if os.environ.get("triggered_by_git_tag", ""):
        return True
    return os.environ.get("requester", "") in NOTIFY_REQUESTERS


def send_slack_notification(message: dict) -> None:
    webhook_url = os.environ.get("SLACK_WEBHOOK_URL")
    if not webhook_url:
        logger.warning("SLACK_WEBHOOK_URL environment variable is not set, skipping Slack notification")
        return

    response = requests.post(
        webhook_url,
        json=message,
        headers={"Content-Type": "application/json"},
        timeout=30,
    )
    response.raise_for_status()
    logger.info("Slack notification sent successfully")


def scan_image(args) -> list[ScanResult]:
    scenario = get_scenario_from_arg(args.build_scenario)
    scan_targets = resolve_scan_targets(args.image, scenario, args.version)
    trivy_bin = get_trivy_binary()

    os.makedirs(args.output_dir, exist_ok=True)

    results = []
    for image_ref, platforms in scan_targets:
        platforms = args.platform.split(",") if args.platform else platforms
        for platform in platforms:
            sanitized = f"{image_ref.rpartition('/')[2]}-{platform}".replace("/", "-").replace(":", "-")
            output_file = os.path.join(args.output_dir, f"trivy-{sanitized}.json")
            results.append(run_trivy_scan(trivy_bin, image_ref, platform, args.severity, output_file))

    return results


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Scan container images built by the release pipeline for CVEs using trivy "
        "and notify Slack when vulnerabilities are detected."
    )
    parser.add_argument(
        "image",
        metavar="image",
        type=str,
        help="Image name to scan, as defined in build_info.json (e.g. operator, agent, ops-manager)",
    )
    parser.add_argument(
        "-b",
        "--build-scenario",
        required=True,
        type=str,
        choices=SUPPORTED_SCENARIOS,
        help="Build scenario used to resolve image repositories and platforms from build_info.json",
    )
    parser.add_argument(
        "-v",
        "--version",
        required=True,
        type=str,
        help=f"Image version (tag) to scan. For the agent image, '{AGENT_ALL}'/'{AGENT_CURRENT}' "
        "expand to the currently used agent versions.",
    )
    parser.add_argument(
        "-p",
        "--platform",
        type=str,
        help="Override the platforms to scan instead of resolving them from build_info.json. "
        "Comma-separated, e.g. linux/amd64,linux/arm64",
    )
    parser.add_argument(
        "--severity",
        type=str,
        default=DEFAULT_SEVERITIES,
        help=f"Comma-separated severities reported by trivy (default: {DEFAULT_SEVERITIES})",
    )
    parser.add_argument(
        "--output-dir",
        type=str,
        default=DEFAULT_OUTPUT_DIR,
        help=f"Directory where raw trivy JSON reports are written (default: {DEFAULT_OUTPUT_DIR})",
    )
    parser.add_argument(
        "--fail-on-findings",
        action="store_true",
        help="Exit with a non-zero code when vulnerabilities are found. By default the scan "
        "is notify-only and never blocks the build-and-publish pipeline.",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print the Slack message to stdout instead of sending it",
    )
    args = parser.parse_args()

    results = scan_image(args)

    print_stdout_report(results)

    total_vulnerabilities = sum(len(r.vulnerabilities) for r in results)
    if total_vulnerabilities == 0:
        logger.info(f"No vulnerabilities found in '{args.image}' images")
        return 0

    logger.warning(f"Found {total_vulnerabilities} vulnerabilities in '{args.image}' images")

    message = format_slack_message(args.image, get_scenario_from_arg(args.build_scenario), results)
    if args.dry_run:
        print("\n--- DRY RUN: Would send this message to Slack ---")
        print(json.dumps(message, indent=2))
    elif should_notify():
        send_slack_notification(message)
    else:
        logger.info(
            f"Skipping Slack notification (requester '{os.environ.get('requester', '')}' is not a mainline build)"
        )

    return 1 if args.fail_on_findings else 0


if __name__ == "__main__":
    sys.exit(main())

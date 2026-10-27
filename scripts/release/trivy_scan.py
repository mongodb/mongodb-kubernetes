#!/usr/bin/env python3
"""
CVE scanning for freshly built and published container images.

Scans images produced by the build-and-publish pipeline (scripts/release/pipeline.py)
with trivy and notifies Slack (#k8s-enterprise-builds) with a single message listing
the scanned images that have fixable CRITICAL/HIGH vulnerabilities, one line per image,
split into fixable and not fixable, with a link to the report. Images without fixable
vulnerabilities are not in the Slack message (and no message is sent if there are none),
but they are still printed to stdout. The full raw trivy JSON is uploaded to S3 and
linked from the message.

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
    TASK_ID: Evergreen task id, used to link the reports in the Slack message
    execution: Evergreen task execution, used to link the right execution of the task
    TRIVY_REPORTS_AWS_KEY, TRIVY_REPORTS_AWS_SECRET: credentials used to presign the links (valid for 7
        days) to the raw JSON report of each image uploaded to S3. Without them the message links to the
        files of the task instead. They are deliberately not AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY so
        that they do not interfere with the registry authentication of trivy.
    TRIVY_REPORTS_REGION: region of the reports bucket (default: us-east-1)

When --version is not given, the version of each image is resolved from the
environment the same way scripts/release/pipeline.sh does (OPERATOR_VERSION, OM_VERSION,
READINESS_PROBE_VERSION, VERSION_UPGRADE_HOOK_VERSION, AGENT_VERSION_OVERRIDE/AGENT_VERSION,
OM_VERSIONS/OM_VERSION). Several space-separated versions can be given, e.g. OM_VERSIONS="7.0.23 8.0.27".

Usage:
    python scripts/release/trivy_scan.py operator init-database agent --build-scenario staging
    python scripts/release/trivy_scan.py operator --build-scenario staging --version <sha>
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass, field

import boto3
import requests
from botocore.config import Config

from lib.base_logger import logger
from scripts.release.agent.agents_to_rebuild import get_currently_used_agents
from scripts.release.argparse_utils import get_scenario_from_arg
from scripts.release.build.build_info import AGENT_IMAGE, load_build_info
from scripts.release.build.build_scenario import SUPPORTED_SCENARIOS, BuildScenario, is_dryrun

# Slack rejects (HTTP 400) Block Kit sections whose text is longer than 3000 characters
SLACK_SECTION_MAX_CHARS = 2900

# Where the raw trivy JSON reports are uploaded by the trivy_scan function (s3.put in
# .evergreen-functions.yml): s3://<bucket>/<prefix>/<task_id>/<execution>/<file>. Keep in sync with it.
REPORTS_BUCKET = "operator-e2e-artifacts"
REPORTS_PREFIX = "trivy-reports"
REPORTS_DEFAULT_REGION = "us-east-1"
# Maximum validity of a presigned URL signed with static AWS credentials
REPORT_LINK_VALIDITY_SECONDS = 7 * 24 * 3600
ECR_HOST_PATTERN = re.compile(r"^(?P<account>\d+)\.dkr\.ecr\.(?P<region>[a-z0-9-]+)\.amazonaws\.com$")
PREFERRED_REPORT_PLATFORM = "linux/amd64"
DEFAULT_SEVERITIES = "CRITICAL,HIGH"
DEFAULT_OUTPUT_DIR = "trivy-reports"
TRIVY_TIMEOUT = "15m"

# Agent builds use the special versions "all"/"current" instead of a single version
AGENT_ALL = "all"
AGENT_CURRENT = "current"

# Environment variables holding the version of each image, in order of precedence.
# Mirrors scripts/release/pipeline.sh; images not listed use OPERATOR_VERSION.
# A value may hold several space-separated versions (e.g. OM_VERSIONS="7.0.23 8.0.27 9.0.1").
IMAGE_VERSION_ENV = {
    AGENT_IMAGE: ("AGENT_VERSION_OVERRIDE", "AGENT_VERSION"),
    "ops-manager": ("OM_VERSIONS", "OM_VERSION"),
    "readiness-probe": ("READINESS_PROBE_VERSION",),
    "upgrade-hook": ("VERSION_UPGRADE_HOOK_VERSION",),
}
DEFAULT_VERSION_ENV = ("OPERATOR_VERSION",)

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

    @property
    def key(self) -> tuple[str, str, str]:
        return (self.id, self.pkg_name, self.installed_version)


@dataclass
class ScanResult:
    image_ref: str
    platform: str
    vulnerabilities: list[Vulnerability] = field(default_factory=list)
    # digest (sha256:...) the image tag points to in the registry, as reported by trivy
    image_digest: str = ""
    # raw trivy JSON report file name of each scanned platform
    report_files: dict[str, str] = field(default_factory=dict)

    @property
    def report_file(self) -> str:
        """The report to link to: the one of the preferred platform if scanned, else the first one."""
        if PREFERRED_REPORT_PLATFORM in self.report_files:
            return self.report_files[PREFERRED_REPORT_PLATFORM]
        return next(iter(self.report_files.values()), "")

    @property
    def fixable_count(self) -> int:
        return sum(1 for v in self.vulnerabilities if v.is_fixed)

    @property
    def unfixable_count(self) -> int:
        return sum(1 for v in self.vulnerabilities if not v.is_fixed)

    @property
    def total_count(self) -> int:
        return len(self.vulnerabilities)


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


def _override_registry_prefix(repo: str) -> str:
    """Redirect a repository to the override registry in dryrun-release scenarios."""
    override = os.environ.get("REGISTRY", "").strip()
    if not override:
        raise RuntimeError("REGISTRY is unset — refusing to guess the registry for dryrun-release scans")
    return f"{override}/{repo.rsplit('/', 1)[-1]}"


def resolve_image_versions(image: str) -> list[str]:
    """Resolve the versions (tags) of an image from the environment, like pipeline.sh does."""
    env_names = IMAGE_VERSION_ENV.get(image, DEFAULT_VERSION_ENV)
    for name in env_names:
        versions = os.environ.get(name, "").split()
        if versions:
            return versions
    raise ValueError(f"Cannot resolve the version of image '{image}': set {' or '.join(env_names)} or pass --version")


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

    if is_dryrun():
        repository = _override_registry_prefix(image_info.repository)
    else:
        repository = image_info.repository

    return [(f"{repository}:{v}", image_info.platforms) for v in versions]


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

    result = parse_trivy_report(report, image_ref, platform)
    result.report_files = {platform: os.path.basename(output_file)}
    return result


def get_image_digest(report: dict) -> str:
    """The digest the scanned tag points to in the registry, from the RepoDigests ('repo@sha256:...')."""
    for repo_digest in (report.get("Metadata") or {}).get("RepoDigests") or []:
        _, separator, digest = repo_digest.partition("@")
        if separator and digest:
            return digest
    return ""


def parse_trivy_report(report: dict, image_ref: str, platform: str) -> ScanResult:
    """Convert a trivy JSON report into a ScanResult, deduplicating findings.

    Every CRITICAL/HIGH finding is kept, whether or not a fix is available. The
    report distinguishes fixable from not fixable findings via
    ``Vulnerability.is_fixed``. The full raw trivy JSON is still written to disk
    and uploaded to S3 for investigation.
    """
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
                    fixed_version=(vulnerability.get("FixedVersion") or "").strip(),
                    title=vulnerability.get("Title", ""),
                    primary_url=vulnerability.get("PrimaryURL", ""),
                )
            )

    return ScanResult(
        image_ref=image_ref,
        platform=platform,
        vulnerabilities=vulnerabilities,
        image_digest=get_image_digest(report),
    )


def aggregate_by_image(results: list[ScanResult]) -> list[ScanResult]:
    """Merge the per-platform results of each image into a single result.

    The same image is scanned once per platform, so findings are unioned by
    (CVE, package, installed version) to avoid counting a vulnerability once per
    platform. Results are returned in first-seen image order.
    """
    merged: dict[str, ScanResult] = {}
    for result in results:
        aggregate = merged.get(result.image_ref)
        if aggregate is None:
            aggregate = ScanResult(image_ref=result.image_ref, platform="")
            merged[result.image_ref] = aggregate

        aggregate.image_digest = aggregate.image_digest or result.image_digest
        aggregate.report_files.update(result.report_files)

        seen = {v.key for v in aggregate.vulnerabilities}
        for vulnerability in result.vulnerabilities:
            if vulnerability.key not in seen:
                seen.add(vulnerability.key)
                aggregate.vulnerabilities.append(vulnerability)

    return list(merged.values())


def short_image_name(image_ref: str) -> str:
    """'registry/repo/name:tag' -> 'name:tag'."""
    return image_ref.rpartition("/")[2]


def get_task_files_url() -> str:
    """Link to the files of the Evergreen task, where the raw trivy JSON reports are uploaded."""
    task_id = os.environ.get("TASK_ID", "")
    if not task_id:
        return ""
    url = f"https://spruce.mongodb.com/task/{task_id}/files"
    execution = os.environ.get("execution", "")
    return f"{url}?execution={execution}" if execution else url


def get_report_url(report_file: str) -> str:
    """Link to the raw trivy JSON report of an image.

    The reports are uploaded to a private bucket, so the link is presigned with the credentials
    in TRIVY_REPORTS_AWS_KEY/TRIVY_REPORTS_AWS_SECRET and expires after 7 days. When it cannot be
    generated (no credentials, nothing to link to) the link to the files of the task is returned.
    """
    task_id = os.environ.get("TASK_ID", "")
    key = os.environ.get("TRIVY_REPORTS_AWS_KEY", "")
    secret = os.environ.get("TRIVY_REPORTS_AWS_SECRET", "")
    if not (report_file and task_id and key and secret):
        return get_task_files_url()

    try:
        client = boto3.client(
            "s3",
            aws_access_key_id=key,
            aws_secret_access_key=secret,
            region_name=os.environ.get("TRIVY_REPORTS_REGION") or REPORTS_DEFAULT_REGION,
            config=Config(signature_version="s3v4"),
        )
        return client.generate_presigned_url(
            "get_object",
            Params={
                "Bucket": REPORTS_BUCKET,
                "Key": f"{REPORTS_PREFIX}/{task_id}/{os.environ.get('execution') or '0'}/{report_file}",
            },
            ExpiresIn=REPORT_LINK_VALIDITY_SECONDS,
        )
    except Exception as e:  # the link is a nicety, never fail the report because of it
        logger.warning(f"Could not generate the link to the report {report_file}: {e}")
        return get_task_files_url()


def get_registry_url(image_ref: str, digest: str = "") -> str:
    """Link to the entry of the image in its registry (ECR console, quay.io); empty for other registries.

    Links to the exact image when its digest is known, else to the tag (quay.io) or the repository (ECR).
    """
    reference, _, tag = image_ref.rpartition(":")
    if "/" in tag or not reference:
        reference, tag = image_ref, ""
    host, _, repository = reference.partition("/")

    ecr = ECR_HOST_PATTERN.match(host)
    if ecr:
        account, region = ecr.group("account"), ecr.group("region")
        url = f"https://{region}.console.aws.amazon.com/ecr/repositories/private/{account}/{repository}"
        return f"{url}/_/image/{digest}/details?region={region}" if digest else f"{url}?region={region}"
    if host == "quay.io":
        url = f"https://quay.io/repository/{repository}"
        if digest:
            return f"{url}/manifest/{digest}"
        return f"{url}?tab=tags&tag={tag}" if tag else url
    return ""


def summarize_result(result: ScanResult, with_unfixable: bool = True) -> str:
    """One-line summary, e.g. 'mongodb-agent:1.0: 73 vulnerabilities: 57 fixable, 16 not fixable'."""
    summary = (
        f"{short_image_name(result.image_ref)}: {result.total_count} vulnerabilities: {result.fixable_count} fixable"
    )
    return f"{summary}, {result.unfixable_count} not fixable" if with_unfixable else summary


def slack_line(result: ScanResult) -> str:
    """'- <registry link|image:tag>: N vulnerabilities: M fixable (<report link|report>)'."""
    name = short_image_name(result.image_ref)
    registry_url = get_registry_url(result.image_ref, result.image_digest)
    summary = summarize_result(result, with_unfixable=False)
    if registry_url:
        summary = summary.replace(name, f"<{registry_url}|{name}>", 1)

    report_url = get_report_url(result.report_file)
    return f"- {summary}" + (f" (<{report_url}|report>)" if report_url else "")


def print_stdout_report(results: list[ScanResult]) -> None:
    """Print one line per image that has findings. Clean images are omitted."""
    for result in results:
        if result.total_count == 0:
            continue
        print(f"- {summarize_result(result)}")


def split_into_sections(intro: str, lines: list[str], max_chars: int = SLACK_SECTION_MAX_CHARS) -> list[str]:
    """Group the lines into texts of at most max_chars characters, the first one starting with the intro."""
    sections = []
    current = intro
    for line in lines:
        candidate = f"{current}\n{line}"
        if len(candidate) > max_chars and current != intro:
            sections.append(current)
            current = line
        else:
            current = candidate
    sections.append(current)
    return sections


def format_slack_message(scenario: BuildScenario, results: list[ScanResult]) -> dict:
    """Format the scan findings of all scanned images as a single Slack Block Kit message.

    The body is a list with one line per image that has fixable vulnerabilities (images
    without findings, or with only unfixable ones, are omitted). Each line links the image
    to its entry in the registry and has a link to the raw trivy JSON report of the image.
    Per-CVE details are not in the message. The lines are spread over as many section
    blocks as needed to stay within Slack's limit for a section's text.
    """
    lines = [slack_line(r) for r in results if r.fixable_count > 0]
    intro = f"Fixable vulnerabilities found in newly built images (build scenario: `{scenario}`):"

    blocks = [{"type": "header", "text": {"type": "plain_text", "text": "CVE scan report"}}]
    blocks += [
        {"type": "section", "text": {"type": "mrkdwn", "text": text}} for text in split_into_sections(intro, lines)
    ]
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
    if not response.ok:
        logger.error(f"Slack rejected the notification ({response.status_code}): {response.text}")
    response.raise_for_status()
    logger.info("Slack notification sent successfully")


def scan_images(args) -> list[ScanResult]:
    """Scan all the requested images. A failure scanning one image is logged and does not stop the others."""
    scenario = get_scenario_from_arg(args.build_scenario)
    trivy_bin = get_trivy_binary()

    os.makedirs(args.output_dir, exist_ok=True)

    results = []
    for image in args.images:
        try:
            versions = [args.version] if args.version else resolve_image_versions(image)
            scan_targets = [t for version in versions for t in resolve_scan_targets(image, scenario, version)]
        except ValueError as e:
            logger.error(f"Skipping the scan of '{image}': {e}")
            continue

        for image_ref, platforms in scan_targets:
            platforms = args.platform.split(",") if args.platform else platforms
            for platform in platforms:
                sanitized = f"{short_image_name(image_ref)}-{platform}".replace("/", "-").replace(":", "-")
                output_file = os.path.join(args.output_dir, f"trivy-{sanitized}.json")
                try:
                    results.append(run_trivy_scan(trivy_bin, image_ref, platform, args.severity, output_file))
                except RuntimeError as e:
                    logger.error(str(e))

    return results


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Scan container images built by the release pipeline for CVEs using trivy "
        "and notify Slack when vulnerabilities are detected."
    )
    parser.add_argument(
        "images",
        metavar="image",
        nargs="+",
        type=str,
        help="Image names to scan, as defined in build_info.json (e.g. operator agent ops-manager). "
        "All of them are reported in a single message.",
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
        type=str,
        help="Image version (tag) to scan; only valid with a single image. By default the version of each "
        f"image is resolved from the environment. For the agent image, '{AGENT_ALL}'/'{AGENT_CURRENT}' "
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
    if args.version and len(args.images) > 1:
        parser.error("--version can only be used with a single image")

    results = aggregate_by_image(scan_images(args))

    print_stdout_report(results)

    total_found = sum(r.total_count for r in results)
    if total_found == 0:
        return 0

    logger.warning(f"Found {total_found} CRITICAL/HIGH vulnerabilities in {' '.join(args.images)} images")

    if not any(r.fixable_count > 0 for r in results):
        logger.info("None of the vulnerabilities is fixable, skipping the Slack notification")
        return 1 if args.fail_on_findings else 0

    message = format_slack_message(get_scenario_from_arg(args.build_scenario), results)
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

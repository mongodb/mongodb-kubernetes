#!/usr/bin/env python3
"""
Bumps the default third-party images used by MongoDBSearch in release.json.

release.json is the single source of truth for the versions:
    search.metricsForwarderVersion  -> otel/opentelemetry-collector-contrib
    search.envoyVersion             -> envoyproxy/envoy
The Helm values, the generated public manifests and config/manager/manager.yaml are derived from it
(scripts/dev/generate_files.sh), so after a bump those files have to be regenerated.

Bump policy (the defaults can be relaxed with flags):
  * only stable tags are considered: ^v?MAJOR.MINOR.PATCH$ (no nightly, rc or arch-suffixed tags);
  * patch-only: the new version must stay within the current MAJOR.MINOR; --allow-minor also permits
    newer minors of the same major (a new major is never picked up automatically);
  * soak: the tag must have been published at least --soak-days days ago (Docker Hub `last_updated`);
    --security skips the soak so that CVE fixes are not delayed.

Usage (from the root of the repository):
    scripts/dev/run_python.sh scripts/dev/bump_third_party_images.py --image otel
    scripts/dev/run_python.sh scripts/dev/bump_third_party_images.py --image envoy --allow-minor --dry-run

When $GITHUB_OUTPUT is set, `<image>_changed` (true/false) and `<image>_version` are appended to it.
The script exits with 0 and leaves release.json untouched when no eligible version exists.
"""

import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
from collections.abc import Callable, Iterable
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone

# NOTE: Docker Hub's `ordering` parameter is inverted: `last_updated` lists the newest tags first, `-last_updated` the oldest.
DOCKER_HUB_TAGS_URL = "https://hub.docker.com/v2/repositories/{repo}/tags?page_size=100&ordering=last_updated"
DEFAULT_SOAK_DAYS = 14
# Safety net against unbounded pagination (100 tags per page).
MAX_PAGES = 100
HTTP_RETRIES = 4
HTTP_TIMEOUT_SECONDS = 30

STABLE_TAG_REGEX = re.compile(r"^(v?)(\d+)\.(\d+)\.(\d+)$")

Version = tuple[int, int, int]


@dataclass(frozen=True)
class ImageSpec:
    repo: str
    release_json_key: str  # key under "search" in release.json


IMAGES: dict[str, ImageSpec] = {
    "otel": ImageSpec(repo="otel/opentelemetry-collector-contrib", release_json_key="metricsForwarderVersion"),
    "envoy": ImageSpec(repo="envoyproxy/envoy", release_json_key="envoyVersion"),
}


@dataclass(frozen=True)
class Candidate:
    tag: str
    version: Version
    last_updated: datetime


@dataclass(frozen=True)
class Decision:
    """Outcome of the policy: new_tag is None when no bump should happen."""

    new_tag: str | None
    reason: str


def parse_stable_tag(tag: str) -> tuple[str, Version] | None:
    """Returns (prefix, (major, minor, patch)) for stable tags such as `0.165.0` or `v1.37.11`, None otherwise."""
    match = STABLE_TAG_REGEX.match(tag)
    if match is None:
        return None
    prefix, major, minor, patch = match.groups()
    return prefix, (int(major), int(minor), int(patch))


def parse_timestamp(value: str) -> datetime:
    """Parses Docker Hub timestamps such as 2026-11-07T10:11:12.123456Z into timezone-aware datetimes."""
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed


def candidates_from_tags(tags: Iterable[dict]) -> list[Candidate]:
    """Converts Docker Hub tag objects into candidates, dropping everything that is not a stable tag."""
    candidates = []
    for entry in tags:
        parsed = parse_stable_tag(entry.get("name", ""))
        last_updated = entry.get("last_updated")
        if parsed is None or not last_updated:
            continue
        candidates.append(Candidate(tag=entry["name"], version=parsed[1], last_updated=parse_timestamp(last_updated)))
    return candidates


def select_version(
    current_tag: str,
    candidates: Iterable[Candidate],
    now: datetime,
    allow_minor: bool = False,
    soak_days: int = DEFAULT_SOAK_DAYS,
    security: bool = False,
) -> Decision:
    """Picks the highest eligible version according to the bump policy. Pure function, no I/O."""
    current = parse_stable_tag(current_tag)
    if current is None:
        raise ValueError(f"current version {current_tag!r} is not a stable MAJOR.MINOR.PATCH tag")
    prefix, current_version = current
    soak = timedelta(days=soak_days)

    newer_in_scope: list[Candidate] = []
    for candidate in candidates:
        # Keep the tag style (with or without the "v" prefix) of the current version.
        if candidate.tag.startswith("v") != (prefix == "v"):
            continue
        if candidate.version <= current_version:
            continue
        if candidate.version[0] != current_version[0]:
            continue  # never bump the major automatically
        if not allow_minor and candidate.version[:2] != current_version[:2]:
            continue
        newer_in_scope.append(candidate)

    if not newer_in_scope:
        scope = "minor/patch" if allow_minor else f"patch of {current_version[0]}.{current_version[1]}"
        return Decision(None, f"no newer {scope} version than {current_tag} is available")

    newer_in_scope.sort(key=lambda c: c.version, reverse=True)
    if security:
        best = newer_in_scope[0]
        return Decision(best.tag, f"{best.tag} selected: security mode, soak of {soak_days} days skipped")

    for candidate in newer_in_scope:
        age = now - candidate.last_updated
        if age >= soak:
            skipped = [c.tag for c in newer_in_scope if c.version > candidate.version]
            note = f" (newer {', '.join(skipped)} still soaking)" if skipped else ""
            return Decision(candidate.tag, f"{candidate.tag} selected: published {age.days} days ago{note}")

    youngest_wait = min(soak - (now - c.last_updated) for c in newer_in_scope)
    return Decision(
        None,
        f"newer versions ({', '.join(c.tag for c in newer_in_scope)}) are all younger than the {soak_days}-day soak; "
        f"the next one becomes eligible in about {youngest_wait.days + 1} day(s). "
        "Re-run with --security to skip the soak for a security fix",
    )


def http_get_json(url: str, opener: Callable = urllib.request.urlopen) -> dict:
    """GETs a JSON document, retrying on transient errors with exponential backoff."""
    request = urllib.request.Request(url, headers={"Accept": "application/json", "User-Agent": "third-party-bump/1.0"})
    for attempt in range(HTTP_RETRIES):
        try:
            with opener(request, timeout=HTTP_TIMEOUT_SECONDS) as response:
                return json.load(response)
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as e:
            if isinstance(e, urllib.error.HTTPError) and e.code < 500 and e.code != 429:
                raise
            if attempt == HTTP_RETRIES - 1:
                raise
            delay = 2**attempt
            print(f"request to {url} failed ({e}); retrying in {delay}s", file=sys.stderr)
            time.sleep(delay)
    raise AssertionError("unreachable")


def fetch_tags(
    repo: str,
    current_tag: str,
    allow_minor: bool,
    fetch_json: Callable[[str], dict] = http_get_json,
) -> list[dict]:
    """Lists Docker Hub tags of repo, newest first, following pagination.

    The listing is narrowed server-side (substring match on the tag name) to tags containing MAJOR.MINOR. - or just
    MAJOR. with --allow-minor - because images such as envoy publish many dev tags daily. In any case, paging stops
    after the page that contains the current tag: everything after it was published earlier.
    """
    url: str | None = DOCKER_HUB_TAGS_URL.format(repo=repo)
    parsed = parse_stable_tag(current_tag)
    if parsed is not None:
        prefix, (major, minor, _) = parsed
        url += f"&name={prefix}{major}." if allow_minor else f"&name={prefix}{major}.{minor}."

    tags: list[dict] = []
    pages = 0
    while url and pages < MAX_PAGES:
        page = fetch_json(url)
        pages += 1
        results = page.get("results", [])
        tags.extend(results)
        if any(entry.get("name") == current_tag for entry in results):
            break
        url = page.get("next")
    return tags


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


def read_release_json(path: str) -> dict:
    with open(path) as fd:
        return json.load(fd)


def write_release_json(path: str, data: dict) -> None:
    # Same formatting as scripts/evergreen/release/update_release.py so that the file does not churn.
    with open(path, "w") as fd:
        json.dump(data, fd, indent=2)
        fd.write("\n")


def write_github_output(image: str, new_tag: str | None) -> None:
    output_path = os.environ.get("GITHUB_OUTPUT")
    if not output_path:
        return
    with open(output_path, "a") as fd:
        fd.write(f"{image}_changed={'true' if new_tag else 'false'}\n")
        fd.write(f"{image}_version={new_tag or ''}\n")


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Bump a default third-party MongoDBSearch image in release.json according to the bump policy.",
    )
    parser.add_argument("--image", required=True, choices=sorted(IMAGES), help="image to bump")
    parser.add_argument(
        "--allow-minor", action="store_true", help="also allow newer minor versions (default: patch-only)"
    )
    parser.add_argument(
        "--soak-days",
        type=int,
        default=DEFAULT_SOAK_DAYS,
        help=f"minimum age of a tag in days (default: {DEFAULT_SOAK_DAYS})",
    )
    parser.add_argument("--security", action="store_true", help="skip the soak so that security fixes are not delayed")
    parser.add_argument("--dry-run", action="store_true", help="print the decision without modifying release.json")
    parser.add_argument("--release-json", default="release.json", help="path to release.json (default: release.json)")
    args = parser.parse_args(argv)
    if args.soak_days < 0:
        parser.error("--soak-days must not be negative")
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    spec = IMAGES[args.image]

    release = read_release_json(args.release_json)
    current_tag = release["search"][spec.release_json_key]
    print(f"{spec.repo}: current version {current_tag}")
    print(
        f"policy: {'minor and patch' if args.allow_minor else 'patch-only'}, "
        f"soak {'skipped (security)' if args.security else f'{args.soak_days} days'}"
    )

    tags = fetch_tags(spec.repo, current_tag, args.allow_minor)
    candidates = candidates_from_tags(tags)
    print(f"considering {len(candidates)} stable tags out of {len(tags)} listed")

    decision = select_version(
        current_tag,
        candidates,
        now=utcnow(),
        allow_minor=args.allow_minor,
        soak_days=args.soak_days,
        security=args.security,
    )

    if decision.new_tag is None:
        print(f"decision: no change - {decision.reason}")
        write_github_output(args.image, None)
        return 0

    print(f"decision: bump {current_tag} -> {decision.new_tag} - {decision.reason}")
    if args.dry_run:
        print("dry-run: release.json not modified")
        write_github_output(args.image, None)
        return 0

    release["search"][spec.release_json_key] = decision.new_tag
    write_release_json(args.release_json, release)
    print(f"updated {args.release_json}: search.{spec.release_json_key} = {decision.new_tag}")
    write_github_output(args.image, decision.new_tag)
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""
Bumps the MongoDBSearch Envoy and OTel collector images in release.json to the newest stable Docker Hub tag (never
downgrades). A new minor or major must be at least 7 days old; patches of the current or an already soaked minor are
taken immediately. Also adds a changelog entry and regenerates the derived files.

Usage (from the repository root):
    PYTHONPATH=. python -m scripts.dev.bump_third_party_images [--dry-run]
"""

import argparse
import json
import re
import subprocess
import sys
import urllib.request
from datetime import datetime, timedelta, timezone

from scripts.evergreen.release.update_helm_values_files import (
    ENVOY_IMAGE_REPO,
    METRICS_FORWARDER_IMAGE_REPO,
    load_release,
    update_search_images,
)
from scripts.release.changelog import ChangeKind, get_changelog_filename

# Docker Hub is queried once per published release line at or above the current one (the name filter is a substring
# match and also hits variant/nightly tags).
TAGS_URL = "https://hub.docker.com/v2/repositories/{repo}/tags?page_size=100&ordering=last_updated&name={prefix}"
AUTH_URL = "https://auth.docker.io/token?service=registry.docker.io&scope=repository:{repo}:pull"
REGISTRY = "https://registry-1.docker.io"
SOAK = timedelta(days=7)
# Docker Hub refuses anonymous paging past page 10.
MAX_PAGES = 10
# (release.json key under "search", docker repo, human readable name)
IMAGES = [
    ("envoyVersion", ENVOY_IMAGE_REPO, "Envoy"),
    ("metricsForwarderVersion", METRICS_FORWARDER_IMAGE_REPO, "metrics forwarder"),
]


def version_key(tag: str) -> tuple:
    return tuple(int(p) for p in tag.lstrip("v").split("."))


def release_lines(repo: str, pattern: re.Pattern) -> set[tuple[int, int]]:
    """Returns the (major, minor) lines of all stable tags; the registry lists every name without a paging cap."""
    with urllib.request.urlopen(AUTH_URL.format(repo=repo), timeout=30) as resp:
        token = json.load(resp)["token"]
    lines, url = set(), f"{REGISTRY}/v2/{repo}/tags/list"
    while url:
        request = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"})
        with urllib.request.urlopen(request, timeout=60) as resp:
            names = json.load(resp)["tags"] or []
            link = re.search(r"<([^>]+)>;\s*rel=\"next\"", resp.headers.get("Link", ""))
        lines.update(version_key(name)[:2] for name in names if pattern.match(name))
        url = REGISTRY + link.group(1) if link else None
    return lines


def stable_tags(repo: str, prefix: str, pattern: re.Pattern) -> dict[str, datetime]:
    tags, url = {}, TAGS_URL.format(repo=repo, prefix=prefix)
    for _ in range(MAX_PAGES):
        with urllib.request.urlopen(url, timeout=30) as resp:
            page = json.load(resp)
        for tag in page["results"]:
            if not pattern.match(tag["name"]):
                continue
            pushed = tag.get("tag_last_pushed") or tag["last_updated"]
            tags[tag["name"]] = datetime.fromisoformat(pushed.replace("Z", "+00:00"))
        url = page.get("next")
        if not url:
            return tags
    sys.exit(f"{repo}: more than {MAX_PAGES} pages of tags match {prefix!r}; narrow the query")


def latest_soaked_tag(repo: str, current: str, now: datetime) -> str:
    pattern = re.compile(r"^v\d+\.\d+\.\d+$" if current.startswith("v") else r"^\d+\.\d+\.\d+$")
    v = "v" if current.startswith("v") else ""
    stable = {}
    for major, minor in sorted(line for line in release_lines(repo, pattern) if line >= version_key(current)[:2]):
        stable.update(
            {
                name: pushed
                for name, pushed in stable_tags(repo, f"{v}{major}.{minor}.", pattern).items()
                if version_key(name)[:2] == (major, minor)
            }
        )
    if not stable:
        sys.exit(f"{repo}: no stable tag matching {pattern.pattern} found; the Docker Hub tag listing may have changed")
    # A minor's release time is its earliest stable tag push (a missing .0 tag can only make it wait longer).
    first_push = {}
    for name, pushed in stable.items():
        minor = version_key(name)[:2]
        first_push[minor] = min(pushed, first_push.get(minor, pushed))
    # The minor we are on counts as soaked, so its patches (where upstream ships CVE fixes) are taken right away.
    soaked = {version_key(current)[:2]} | {minor for minor, pushed in first_push.items() if now - pushed >= SOAK}
    return max([current, *(name for name in stable if version_key(name)[:2] in soaked)], key=version_key)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--dry-run", action="store_true", help="only print the planned changes")
    args = parser.parse_args()

    release = load_release()
    now, bullets = datetime.now(timezone.utc), []
    for key, repo, name in IMAGES:
        old = release["search"][key]
        new = latest_soaked_tag(repo, old, now)
        if new == old:
            print(f"{repo}: up to date ({old})")
            continue
        print(f"{repo}: {old} -> {new}")
        release["search"][key] = new
        bullets.append(f"* **MongoDBSearch**: Updated the default {name} image from `{repo}:{old}` to `{repo}:{new}`.")

    if not bullets or args.dry_run:
        return
    with open("release.json", "w") as f:
        json.dump(release, f, indent=2)
        f.write("\n")
    changelog = "changelog/" + get_changelog_filename(
        f"search third party images bump {now:%H%M%S}",
        ChangeKind.OTHER,
        now,
    )
    with open(changelog, "x") as f:
        f.write(f"---\nkind: other\ndate: {now:%Y-%m-%d}\n---\n\n" + "\n".join(bullets) + "\n")
    update_search_images(release)
    subprocess.run(["scripts/dev/generate_files.sh", "generate_standalone_yaml"], check=True)


if __name__ == "__main__":
    main()

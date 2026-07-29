"""Download a released `kubectl-mongodb` plugin of a given major line, for the E2E test image.

Upgrade tests start from a released operator, and its member clusters are provisioned with a released
plugin of the matching major — never the branch build.

Assets come from the public GitHub releases of `mongodb/mongodb-kubernetes`.
The newest release of the line is resolved at fetch time rather than pinned; set
`RELEASED_KUBECTL_PLUGIN_VERSION` to force a specific version.
"""

import argparse
import hashlib
import io
import os
import tarfile

import requests
import semver
from github import Github

from lib.base_logger import logger
from scripts.release.build.image_build_configuration import SUPPORTED_PLATFORMS
from scripts.release.kubectl_mongodb.utils import GITHUB_REPO, GITHUB_TOKEN

PLUGIN_VERSION_ENV = "RELEASED_KUBECTL_PLUGIN_VERSION"

CHECKSUMS_ASSET = "checksums.txt"
# Name of the executable inside the release tarball.
PLUGIN_BINARY_NAME = "kubectl-mongodb"


def local_tests_released_plugin_path(major: int, arch_name: str) -> str:
    """Where the test image build expects the binary. Suffixed so it sits beside the branch build."""
    return f"docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator-mck{major}x_{arch_name}"


def resolve_latest_released_version(major: int) -> str:
    """Highest published (non-draft, non-prerelease) semver release of the given major line."""
    # Unauthenticated access works; a token only raises the API rate limit.
    releases = Github(GITHUB_TOKEN or None).get_repo(GITHUB_REPO).get_releases()

    versions = []
    for release in releases:
        if release.draft or release.prerelease:
            continue
        version = semver.Version.parse(release.tag_name, optional_minor_and_patch=True)
        # Skip anything that is not a plain release tag, e.g. `1.9.1-test`, `1.0.1-release`.
        if version.prerelease or version.build:
            continue
        if version.major == major:
            versions.append(version)

    if not versions:
        raise Exception(f"No published {major}.x release found in {GITHUB_REPO}")

    return str(max(versions))


def asset_url(version: str, asset_name: str) -> str:
    return f"https://github.com/{GITHUB_REPO}/releases/download/{version}/{asset_name}"


def download_asset(version: str, asset_name: str) -> bytes:
    url = asset_url(version, asset_name)
    logger.info(f"Downloading {url}")
    response = requests.get(url, timeout=300)
    response.raise_for_status()
    return response.content


def expected_checksum(checksums: str, asset_name: str) -> str:
    for line in checksums.splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[1] == asset_name:
            return fields[0]
    raise Exception(f"No checksum for {asset_name} in {CHECKSUMS_ASSET}")


def extract_plugin(tarball: bytes, local_path: str) -> None:
    with tarfile.open(fileobj=io.BytesIO(tarball), mode="r:gz") as tar:
        member = tar.extractfile(PLUGIN_BINARY_NAME)
        if member is None:
            raise Exception(f"{PLUGIN_BINARY_NAME} not found in release tarball")
        binary = member.read()

    with open(local_path, "wb") as f:
        f.write(binary)
    os.chmod(local_path, 0o755)


def download_released_kubectl_plugin(major: int, platform: str, version: str) -> None:
    os_name, arch_name = platform.split("/")
    asset_name = f"{PLUGIN_BINARY_NAME}_{version}_{os_name}_{arch_name}.tar.gz"

    tarball = download_asset(version, asset_name)
    checksums = download_asset(version, CHECKSUMS_ASSET).decode()

    actual = hashlib.sha256(tarball).hexdigest()
    expected = expected_checksum(checksums, asset_name)
    if actual != expected:
        raise Exception(f"Checksum mismatch for {asset_name}: expected {expected}, got {actual}")

    local_path = local_tests_released_plugin_path(major, arch_name)
    extract_plugin(tarball, local_path)
    logger.info(f"Installed released kubectl-mongodb plugin {version} ({platform}) at {local_path}")


def main():
    parser = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "-p",
        "--platform",
        metavar="",
        action="store",
        required=True,
        type=str,
        choices=SUPPORTED_PLATFORMS,
        help=f"Platform of the plugin to download. Options: {", ".join(SUPPORTED_PLATFORMS)}.",
    )
    parser.add_argument(
        "-m",
        "--major",
        metavar="",
        action="store",
        required=True,
        type=int,
        help="Major version line to resolve the newest release from.",
    )
    parser.add_argument(
        "-v",
        "--version",
        metavar="",
        action="store",
        default=os.environ.get(PLUGIN_VERSION_ENV, ""),
        type=str,
        help=f"Exact released version to download. Defaults to ${PLUGIN_VERSION_ENV}, or the newest "
        f"published release of the --major line.",
    )
    args = parser.parse_args()

    version = args.version or resolve_latest_released_version(args.major)
    logger.info(f"Using released kubectl-mongodb plugin version {version}")

    download_released_kubectl_plugin(args.major, args.platform, version)


if __name__ == "__main__":
    main()

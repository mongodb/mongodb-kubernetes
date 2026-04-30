"""Update release.json:mongodbOperator to match calculate_next_version output.

Run as a pre-commit hook before update-release-json so that downstream
fields (initDatabaseVersion, initOpsManagerVersion, databaseImageVersion)
are propagated from a freshly-calculated mongodbOperator value.

No CLI flags. Operates against the git repo at DEFAULT_REPOSITORY_PATH
(the current working directory) and the release.json at the repo root,
matching scripts/release/calculate_next_version.py defaults.
"""

import json
import sys

from git import Repo

from scripts.release.constants import (
    DEFAULT_CHANGELOG_PATH,
    DEFAULT_RELEASE_INITIAL_VERSION,
    DEFAULT_REPOSITORY_PATH,
)
from scripts.release.version import calculate_next_version

RELEASE_JSON_PATH = "release.json"


def main() -> None:
    repo = Repo(DEFAULT_REPOSITORY_PATH)
    next_version = calculate_next_version(
        repo,
        DEFAULT_CHANGELOG_PATH,
        None,
        DEFAULT_RELEASE_INITIAL_VERSION,
    )

    with open(RELEASE_JSON_PATH, "r") as f:
        data = json.load(f)

    current = data.get("mongodbOperator")
    if current == next_version:
        print(f"mongodbOperator: {current} (unchanged)")
        return

    data["mongodbOperator"] = next_version

    with open(RELEASE_JSON_PATH, "w") as f:
        json.dump(data, f, indent=2)
        f.write("\n")

    print(f"mongodbOperator: {current} -> {next_version}")


if __name__ == "__main__":
    sys.exit(main() or 0)

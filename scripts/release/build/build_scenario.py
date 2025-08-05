import os
from enum import StrEnum
from git import Repo

from scripts.release.constants import get_initial_commit_sha, get_initial_version, DEFAULT_CHANGELOG_PATH, \
    DEFAULT_REPOSITORY_PATH
from scripts.release.version import calculate_next_version

COMMIT_SHA_LENGTH = 8


class BuildScenario(StrEnum):
    RELEASE = "release"  # Official release triggered by a git tag
    PATCH = "patch"  # CI build for a patch/pull request
    STAGING = "staging"  # CI build from a merge to the master

    def get_version(self, repository_path: str) -> str:
        match self:
            case BuildScenario.PATCH:
                build_id = os.environ["BUILD_ID"]
                if not build_id:
                    raise ValueError(f"BUILD_ID environment variable is not set for `{self}` build scenario")
                return build_id
            case BuildScenario.STAGING:
                repo = Repo(repository_path)
                return repo.head.object.hexsha[:COMMIT_SHA_LENGTH]
            case BuildScenario.RELEASE:
                repo = Repo(repository_path)
                initial_commit_sha = get_initial_commit_sha()
                initial_version = get_initial_version()
                return calculate_next_version(repo, DEFAULT_CHANGELOG_PATH, initial_commit_sha, initial_version)

        raise ValueError(f"Unknown build scenario: {self}")

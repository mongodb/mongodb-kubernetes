from enum import StrEnum

from git import Repo

from lib.base_logger import logger
from scripts.release.constants import triggered_by_git_tag, is_evg_patch, is_running_in_evg, get_version_id, \
    DEFAULT_REPOSITORY_PATH
from scripts.release.version import calculate_next_version

COMMIT_SHA_LENGTH = 8


class BuildScenario(StrEnum):
    RELEASE = "release"  # Official release triggered by a git tag
    MANUAL_RELEASE = "manual_release"  # Manual release, not part of operator release cycle
    PATCH = "patch"  # CI build for a patch/pull request
    STAGING = "staging"  # CI build from a merge to the master
    DEVELOPMENT = "development"  # Local build on a developer machine

    @classmethod
    def infer_scenario_from_environment(cls, repository_path: str = DEFAULT_REPOSITORY_PATH) -> "BuildScenario":
        """Infer the build scenario from environment variables."""
        git_tag = triggered_by_git_tag()
        is_patch = is_evg_patch()
        is_evg = is_running_in_evg()
        patch_id = get_version_id()

        if git_tag:
            # Release scenario and the git tag will be used for promotion process only
            scenario = BuildScenario.RELEASE
            logger.info(f"Build scenario: {scenario} (git_tag: {git_tag})")
        elif is_patch or is_evg:
            scenario = BuildScenario.PATCH
            logger.info(f"Build scenario: {scenario} (patch_id: {patch_id})")
        elif is_evg and not is_patch:
            scenario = BuildScenario.STAGING
            logger.info(f"Build scenario: {scenario} (version_id: {get_staging_version_id(repository_path)}")
        else:
            scenario = BuildScenario.DEVELOPMENT
            logger.info(f"Build scenario: {scenario}")

        return scenario

    def get_version(self, repository_path: str, changelog_sub_path: str, initial_commit_sha: str = None,
                    initial_version: str = None) -> str:
        repo = Repo(repository_path)

        match self:
            case BuildScenario.DEVELOPMENT:
                # When working locally, "version_id" env variable is defined in the generated context file. It is "latest" by
                # default, and can be overridden with OVERRIDE_VERSION_ID
                return "latest"
            case BuildScenario.PATCH:
                patch_id = get_version_id()
                if not patch_id:
                    raise ValueError(f"version_id environment variable is not set for `{self}` build scenario")
                return patch_id
            case BuildScenario.STAGING:
                return get_staging_version_id(repository_path)
            case BuildScenario.RELEASE:
                return calculate_next_version(repo, changelog_sub_path, initial_commit_sha, initial_version)
            case BuildScenario.MANUAL_RELEASE:
                # For manual releases, version must be provided externally (e.g., for ops-manager via om_version env var,
                # for agent via release.json). Return None to indicate version will be set by image-specific logic.
                return None

        raise ValueError(f"Unknown build scenario: {self}")


def get_staging_version_id(repository_path: str):
    repo = Repo(repository_path)
    return repo.head.object.hexsha[:COMMIT_SHA_LENGTH]

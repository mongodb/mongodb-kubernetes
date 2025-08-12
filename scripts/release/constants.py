import os

RELEASE_INITIAL_VERSION_ENV_VAR = "RELEASE_INITIAL_VERSION"
RELEASE_INITIAL_COMMIT_SHA_ENV_VAR = "RELEASE_INITIAL_COMMIT_SHA"

DEFAULT_RELEASE_INITIAL_VERSION = "1.0.0"
DEFAULT_CHANGELOG_PATH = "changelog/"
DEFAULT_REPOSITORY_PATH = "."


def get_initial_version() -> str | None:
    return os.getenv(RELEASE_INITIAL_VERSION_ENV_VAR)


def get_initial_commit_sha() -> str | None:
    return os.getenv(RELEASE_INITIAL_COMMIT_SHA_ENV_VAR)


def triggered_by_git_tag() -> str | None:
    """
    "triggered_by_git_tag" is the name of the tag that triggered this version, if applicable. It will be None for
    all patches except when tagging a commit on GitHub
    :return: tag name if the build was triggered by a git tag, otherwise None.
    """
    return os.getenv("triggered_by_git_tag")


def is_evg_patch() -> bool:
    """
    A patch build is a version not triggered by a commit to a repository. "is_patch" is passed automatically by Evergreen.
    It either runs tasks on a base commit plus some diff if submitted by the CLI or on a git branch if created by
    a GitHub pull request.
    :return: "true" if the running task is in a patch build, otherwise "false".
    """
    return os.getenv("is_patch", "false").lower() == "true"


def is_running_in_evg() -> bool:
    """
    "RUNNING_IN_EVG" is set by us in evg-private-context
    :return: "true" if the script is running in Evergreen, otherwise "false".
    """
    return os.getenv("RUNNING_IN_EVG", "false").lower() == "true"


def get_version_id() -> str | None:
    """
    Get the version ID from the environment variable. This is typically used for patch builds in the Evergreen CI system.
    It is generated automatically for each task run. For example: `6899b7e35bfaee00077db986` for a manual/PR patch,
    or `mongodb_kubernetes_5c5a3accb47bb411682b8c67f225b61f7ad5a619` for a master merge
    :return: version_id (patch ID) or None if not set
    """
    return os.getenv("version_id")

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
    return os.getenv("triggered_by_git_tag")


def is_evg_patch() -> bool:
    return os.getenv("is_patch", "false").lower() == "true"


def is_running_in_evg() -> bool:
    return os.getenv("RUNNING_IN_EVG", "false").lower() == "true"


def get_version_id() -> str | None:
    """
    Get the version ID from the environment variable. This is typically used for patch builds in the Evergreen CI system.
    :return: version_id (patch ID) or None if not set
    """
    return os.getenv("version_id")

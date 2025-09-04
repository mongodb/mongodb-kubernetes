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


def get_version_id() -> str | None:
    """
    Get the version ID from the environment variable. This is typically used for patch builds in the Evergreen CI system.
    It is generated automatically for each task run. For example: `6899b7e35bfaee00077db986` for a manual/PR patch,
    or `mongodb_kubernetes_5c5a3accb47bb411682b8c67f225b61f7ad5a619` for a master merge
    :return: version_id (patch ID) or None if not set
    """
    return os.getenv("version_id")

import json
import sys
from typing import Callable, List

import requests

from lib.base_logger import logger


def load_agent_build_info():
    """Load agent platform mappings from build_info_agent.json"""
    with open("build_info_agent.json", "r") as f:
        return json.load(f)


def _validate_version_exists(
    version: str,
    platforms: List[str],
    base_url: str,
    filename_builder: Callable[[dict, str, str], str],
    version_type: str,
) -> bool:
    """
    Generic validation function for checking if a version exists for all specified platforms.

    Args:
        version: Version to validate
        platforms: List of platforms to check
        base_url: Base URL for downloads
        filename_builder: Function that builds filename from (agent_info, version, platform)
        version_type: Type of version being validated (for logging)
        platform_not_found_action: Action when platform not found ("exit" or "continue")

    Returns:
        True if version exists for all platforms, False otherwise
    """
    agent_info = load_agent_build_info()

    for platform in platforms:
        if platform not in agent_info["platform_mappings"]:
            logger.error(f"Platform {platform} not found in agent mappings, skipping validation")
            sys.exit(1)

        filename = filename_builder(agent_info, version, platform)
        url = f"{base_url}/{filename}"

        try:
            # Use HEAD request to check if URL exists without downloading the file
            response = requests.head(url, timeout=30)
            if response.status_code != 200:
                logger.warning(
                    f"{version_type.title()} version {version} not found for platform {platform} at {url} (HTTP {response.status_code})"
                )
                return False
            logger.debug(f"{version_type.title()} version {version} validated for platform {platform}")
        except requests.RequestException as e:
            logger.warning(f"Failed to validate {version_type} version {version} for platform {platform}: {e}")
            return False

    logger.info(f"{version_type.title()} version {version} validated for all platforms: {platforms}")
    return True


def _build_agent_filenames(agent_info: dict, agent_version: str, platform: str) -> List[str]:
    """Build all possible agent filenames for a given platform and version."""
    mapping = agent_info["platform_mappings"][platform]
    filenames = []
    for agent_suffix in mapping["agent_suffixes"]:
        filename = f"{agent_info['base_names']['agent']}-{agent_version}.{agent_suffix}"
        filenames.append(filename)
    return filenames


def _get_available_platforms_with_fallback(
    version: str,
    platforms: List[str],
    base_url: str,
    filenames_builder: Callable[[dict, str, str], List[str]],
    version_type: str,
) -> List[str]:
    """
    Generic function to get the list of platforms where a version is actually available,
    trying multiple filename possibilities for each platform.

    Args:
        version: Version to check
        platforms: List of platforms to check
        base_url: Base URL for downloads
        filenames_builder: Function that builds list of filenames from (agent_info, version, platform)
        version_type: Type of version being validated (for logging)

    Returns:
        List of platforms where the version exists
    """
    agent_info = load_agent_build_info()
    available_platforms = []

    for platform in platforms:
        if platform not in agent_info["platform_mappings"]:
            logger.warning(f"Platform {platform} not found in agent mappings, skipping")
            continue

        filenames = filenames_builder(agent_info, version, platform)
        platform_found = False

        for filename in filenames:
            url = f"{base_url}/{filename}"

            try:
                response = requests.head(url, timeout=30)
                if response.status_code == 200:
                    available_platforms.append(platform)
                    logger.debug(
                        f"{version_type.title()} version {version} available for platform {platform} using {filename}"
                    )
                    platform_found = True
                    break
                else:
                    logger.debug(
                        f"{version_type.title()} version {version} not found for platform {platform} at {url} (HTTP {response.status_code})"
                    )
            except requests.RequestException as e:
                logger.debug(
                    f"Failed to validate {version_type} version {version} for platform {platform} at {url}: {e}"
                )

        if not platform_found:
            logger.warning(
                f"{version_type.title()} version {version} not found for platform {platform} (tried {len(filenames)} possibilities)"
            )

    return available_platforms


def get_available_platforms_for_agent(agent_version: str, platforms: List[str]) -> List[str]:
    """
    Get the list of platforms where the agent version is actually available.
    Tries multiple RHEL versions for each platform to find working binaries.

    Args:
        agent_version: MongoDB agent version to check
        platforms: List of platforms to check

    Returns:
        List of platforms where the agent version exists
    """
    agent_base_url = (
        "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod"
    )

    return _get_available_platforms_with_fallback(
        version=agent_version,
        platforms=platforms,
        base_url=agent_base_url,
        filenames_builder=_build_agent_filenames,
        version_type="agent",
    )


def _build_tools_filenames(agent_info: dict, tools_version: str, platform: str) -> List[str]:
    """Build all possible tools filenames for a given platform and version."""
    mapping = agent_info["platform_mappings"][platform]
    filenames = []

    # Try the current tools suffix first
    tools_suffix = mapping["tools_suffix"].replace("{TOOLS_VERSION}", tools_version)
    filenames.append(f"{agent_info['base_names']['tools']}-{tools_suffix}")

    # Try the old tools suffix as fallback
    if "tools_suffix_old" in mapping:
        tools_suffix_old = mapping["tools_suffix_old"].replace("{TOOLS_VERSION}", tools_version)
        filenames.append(f"{agent_info['base_names']['tools']}-{tools_suffix_old}")

    return filenames


def get_working_agent_filename(agent_version: str, platform: str) -> str:
    """
    Get the actual working agent filename for a specific platform and version.
    Tries multiple RHEL versions and returns the first one that works.

    Args:
        agent_version: MongoDB agent version to check
        platform: Platform to check

    Returns:
        The working filename, or the first filename if none work
    """
    agent_base_url = (
        "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod"
    )

    agent_info = load_agent_build_info()

    if platform not in agent_info["platform_mappings"]:
        logger.warning(f"Platform {platform} not found in agent mappings")
        return ""

    filenames = _build_agent_filenames(agent_info, agent_version, platform)

    for filename in filenames:
        url = f"{agent_base_url}/{filename}"

        try:
            response = requests.head(url, timeout=30)
            if response.status_code == 200:
                logger.debug(f"Found working agent filename for {platform}: {filename}")
                return filename
        except requests.RequestException:
            continue

    # If none work, return empty string to indicate platform should be skipped
    logger.warning(f"No working agent filename found for {platform}, platform will be skipped")
    return ""


def get_working_tools_filename(tools_version: str, platform: str) -> str:
    """
    Get the actual working tools filename for a specific platform and version.
    Tries multiple RHEL versions and returns the first one that works.

    Args:
        tools_version: MongoDB tools version to check
        platform: Platform to check

    Returns:
        The working filename, or the first filename if none work
    """
    tools_base_url = "https://fastdl.mongodb.org/tools/db"

    agent_info = load_agent_build_info()

    if platform not in agent_info["platform_mappings"]:
        logger.warning(f"Platform {platform} not found in agent mappings")
        return ""

    filenames = _build_tools_filenames(agent_info, tools_version, platform)

    for filename in filenames:
        url = f"{tools_base_url}/{filename}"

        try:
            response = requests.head(url, timeout=30)
            if response.status_code == 200:
                logger.debug(f"Found working tools filename for {platform}: {filename}")
                return filename
        except requests.RequestException:
            continue

    # If none work, return empty string to indicate platform should be skipped
    logger.warning(f"No working tools filename found for {platform}, platform will be skipped")
    return ""


def get_available_platforms_for_tools(tools_version: str, platforms: List[str]) -> List[str]:
    """
    Get the list of platforms where the tools version is actually available.
    Tries multiple RHEL versions for each platform to find working binaries.

    Args:
        tools_version: MongoDB tools version to check
        platforms: List of platforms to check

    Returns:
        List of platforms where the tools version exists
    """
    tools_base_url = "https://fastdl.mongodb.org/tools/db"

    return _get_available_platforms_with_fallback(
        version=tools_version,
        platforms=platforms,
        base_url=tools_base_url,
        filenames_builder=_build_tools_filenames,
        version_type="tools",
    )

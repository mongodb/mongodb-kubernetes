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


def _build_agent_filename(agent_info: dict, agent_version: str, platform: str) -> str:
    """Build agent filename for a given platform and version."""
    mapping = agent_info["platform_mappings"][platform]
    return f"{agent_info['base_names']['agent']}-{agent_version}.{mapping['agent_suffix']}"


def validate_agent_version_exists(agent_version: str, platforms: List[str]) -> bool:
    """
    Validate that the agent version exists for all specified platforms by checking URLs.

    Args:
        agent_version: MongoDB agent version to validate
        platforms: List of platforms to check

    Returns:
        True if agent version exists for all platforms, False otherwise
    """
    agent_base_url = (
        "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod"
    )

    return _validate_version_exists(
        version=agent_version,
        platforms=platforms,
        base_url=agent_base_url,
        filename_builder=_build_agent_filename,
        version_type="agent",
    )


def _get_available_platforms(
    version: str,
    platforms: List[str],
    base_url: str,
    filename_builder: Callable[[dict, str, str], str],
    version_type: str,
) -> List[str]:
    """
    Generic function to get the list of platforms where a version is actually available.

    Args:
        version: Version to check
        platforms: List of platforms to check
        base_url: Base URL for downloads
        filename_builder: Function that builds filename from (agent_info, version, platform)
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

        filename = filename_builder(agent_info, version, platform)
        url = f"{base_url}/{filename}"

        try:
            response = requests.head(url, timeout=30)
            if response.status_code == 200:
                available_platforms.append(platform)
                logger.debug(f"{version_type.title()} version {version} available for platform {platform}")
            else:
                logger.warning(
                    f"{version_type.title()} version {version} not found for platform {platform} at {url} (HTTP {response.status_code})"
                )
        except requests.RequestException as e:
            logger.warning(f"Failed to validate {version_type} version {version} for platform {platform}: {e}")

    return available_platforms


def get_available_platforms_for_agent(agent_version: str, platforms: List[str]) -> List[str]:
    """
    Get the list of platforms where the agent version is actually available.

    Args:
        agent_version: MongoDB agent version to check
        platforms: List of platforms to check

    Returns:
        List of platforms where the agent version exists
    """
    agent_base_url = (
        "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod"
    )

    return _get_available_platforms(
        version=agent_version,
        platforms=platforms,
        base_url=agent_base_url,
        filename_builder=_build_agent_filename,
        version_type="agent",
    )


def _build_tools_filename(agent_info: dict, tools_version: str, platform: str) -> str:
    """Build tools filename for a given platform and version."""
    mapping = agent_info["platform_mappings"][platform]
    tools_suffix = mapping["tools_suffix"].replace("{TOOLS_VERSION}", tools_version)
    return f"{agent_info['base_names']['tools']}-{tools_suffix}"


def validate_tools_version_exists(tools_version: str, platforms: List[str]) -> bool:
    """
    Validate that the tools version exists for all specified platforms by checking URLs.

    Args:
        tools_version: MongoDB tools version to validate
        platforms: List of platforms to check

    Returns:
        True if tools version exists for all platforms, False otherwise
    """
    tools_base_url = "https://fastdl.mongodb.org/tools/db"

    return _validate_version_exists(
        version=tools_version,
        platforms=platforms,
        base_url=tools_base_url,
        filename_builder=_build_tools_filename,
        version_type="tools",
    )


def get_available_platforms_for_tools(tools_version: str, platforms: List[str]) -> List[str]:
    """
    Get the list of platforms where the tools version is actually available.

    Args:
        tools_version: MongoDB tools version to check
        platforms: List of platforms to check

    Returns:
        List of platforms where the tools version exists
    """
    tools_base_url = "https://fastdl.mongodb.org/tools/db"

    return _get_available_platforms(
        version=tools_version,
        platforms=platforms,
        base_url=tools_base_url,
        filename_builder=_build_tools_filename,
        version_type="tools",
    )

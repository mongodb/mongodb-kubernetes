import json
import sys
from typing import Callable, Dict, List

import requests

from lib.base_logger import logger


def _load_agent_build_info():
    with open("build_info_agent.json", "r") as f:
        return json.load(f)


class PlatformConfiguration:
    def __init__(self):
        self._agent_info = None

    @property
    def agent_info(self):
        """Lazy load agent build info to avoid repeated file reads."""
        if self._agent_info is None:
            self._agent_info = _load_agent_build_info()
        return self._agent_info

    def generate_tools_build_args(self, platforms: List[str], tools_version: str) -> Dict[str, str]:
        """
        Generate build arguments for MongoDB tools based on platform mappings.
        The build arguments are based on the agent build info by verifying different
        filename possibilities for each platform.

        Args:
            platforms: List of platforms (e.g., ["linux/amd64", "linux/arm64"])
            tools_version: MongoDB tools version

        Returns:
            Dictionary of build arguments for docker build (tools only)
        """
        build_args = {}

        for platform in platforms:
            if platform not in self.agent_info["platforms"]:
                logger.error(f"Platform {platform} not found in agent mappings, skipping")
                continue

            arch = platform.split("/")[-1]

            # Use the same logic as validation to get the working tools filename
            tools_filename = get_working_tools_filename(tools_version, platform)
            if tools_filename:
                build_args[f"mongodb_tools_version_{arch}"] = tools_filename
            else:
                logger.error(f"No working tools filename found for platform {platform}")
                continue

        return build_args

    def generate_agent_build_args(self, platforms: List[str], agent_version: str, tools_version: str) -> Dict[str, str]:
        """
        Generate build arguments for agent image based on platform mappings.

        Args:
            platforms: List of platforms (e.g., ["linux/amd64", "linux/arm64"])
            agent_version: MongoDB agent version
            tools_version: MongoDB tools version

        Returns:
            Dictionary of build arguments for docker build
        """
        build_args = {}

        for platform in platforms:
            if platform not in self.agent_info["platforms"]:
                logger.warning(f"Platform {platform} not found in agent mappings, skipping")
                continue

            arch = platform.split("/")[-1]

            agent_filename = get_working_agent_filename(agent_version, platform)
            tools_filename = get_working_tools_filename(tools_version, platform)

            # Only add build args if we have valid filenames
            if agent_filename and tools_filename:
                build_args[f"mongodb_agent_version_{arch}"] = agent_filename
                build_args[f"mongodb_tools_version_{arch}"] = tools_filename
                logger.debug(
                    f"Validated agent and tools versions for {platform}: agent={agent_filename}, tools={tools_filename}"
                )
            else:
                logger.warning(f"Skipping build args for {platform} - missing agent or tools filename")
                logger.debug(f"  agent_filename: {agent_filename}")
                logger.debug(f"  tools_filename: {tools_filename}")

        return build_args


# Global instance for backward compatibility and ease of use
_platform_config = PlatformConfiguration()


def generate_tools_build_args(platforms: List[str], tools_version: str) -> Dict[str, str]:
    """Generate build arguments for MongoDB tools based on platform mappings."""
    return _platform_config.generate_tools_build_args(platforms, tools_version)


def generate_agent_build_args(platforms: List[str], agent_version: str, tools_version: str) -> Dict[str, str]:
    """Generate build arguments for agent image based on platform mappings."""
    return _platform_config.generate_agent_build_args(platforms, agent_version, tools_version)


def _build_agent_filenames(agent_info: dict, agent_version: str, platform: str) -> List[str]:
    """Build all possible agent filenames for a given platform and version."""
    mapping = agent_info["platforms"][platform]
    filenames = []
    for agent_suffix in mapping["agent_suffixes"]:
        filename = f"{agent_info['base_names']['agent']}-{agent_version}.{agent_suffix}"
        filenames.append(filename)
    return filenames


def _build_tools_filenames(agent_info: dict, tools_version: str, platform: str) -> List[str]:
    """Build all possible tools filenames for a given platform and version."""
    mapping = agent_info["platforms"][platform]
    filenames = []
    for tools_suffix_template in mapping["tools_suffixes"]:
        tools_suffix = tools_suffix_template.replace("{TOOLS_VERSION}", tools_version)
        filenames.append(f"{agent_info['base_names']['tools']}-{tools_suffix}")

    return filenames


def _validate_url_exists(url: str, timeout: int = 30) -> bool:
    try:
        response = requests.head(url, timeout=timeout)
        return response.status_code == 200
    except requests.RequestException:
        return False


def _find_working_filename(
    version: str,
    platform: str,
    base_url: str,
    filenames_builder: Callable[[dict, str, str], List[str]],
    version_type: str,
) -> str:
    """
    Find the first working filename for a given platform and version.

    Args:
        version: Version to check
        platform: Platform to check
        base_url: Base URL for downloads
        filenames_builder: Function that builds list of filenames from (agent_info, version, platform)
        version_type: Type of version being validated (for logging)

    Returns:
        The working filename, or empty string if none work
    """
    agent_info = _platform_config.agent_info

    if platform not in agent_info["platforms"]:
        logger.warning(f"Platform {platform} not found in agent mappings")
        return ""

    filenames = filenames_builder(agent_info, version, platform)

    for filename in filenames:
        url = f"{base_url}/{filename}"

        if _validate_url_exists(url):
            return filename
        else:
            logger.debug(f"{version_type.title()} version {version} not found for platform {platform} at {url}")

    logger.warning(f"No working {version_type} filename found for {platform}, platform will be skipped")
    return ""


def _get_available_platforms(
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
    available_platforms = []

    for platform in platforms:
        working_filename = _find_working_filename(version, platform, base_url, filenames_builder, version_type)
        if working_filename:
            available_platforms.append(platform)
            logger.debug(
                f"{version_type.title()} version {version} available for platform {platform} using {working_filename}"
            )

    return available_platforms


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

    return _find_working_filename(agent_version, platform, agent_base_url, _build_agent_filenames, "agent")


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

    return _find_working_filename(tools_version, platform, tools_base_url, _build_tools_filenames, "tools")

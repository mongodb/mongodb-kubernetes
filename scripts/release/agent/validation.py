import json
from typing import List

import requests

from lib.base_logger import logger


def load_agent_build_info():
    """Load agent platform mappings from build_info_agent.json"""
    with open("build_info_agent.json", "r") as f:
        return json.load(f)


def validate_agent_version_exists(agent_version: str, platforms: List[str]) -> bool:
    """
    Validate that the agent version exists for all specified platforms by checking URLs.

    Args:
        agent_version: MongoDB agent version to validate
        platforms: List of platforms to check

    Returns:
        True if agent version exists for all platforms, False otherwise
    """
    agent_info = load_agent_build_info()
    agent_base_url = (
        "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod"
    )

    for platform in platforms:
        if platform not in agent_info["platform_mappings"]:
            logger.warning(f"Platform {platform} not found in agent mappings, skipping validation")
            continue

        mapping = agent_info["platform_mappings"][platform]
        agent_filename = f"{agent_info['base_names']['agent']}-{agent_version}.{mapping['agent_suffix']}"
        agent_url = f"{agent_base_url}/{agent_filename}"

        try:
            # Use HEAD request to check if URL exists without downloading the file
            response = requests.head(agent_url, timeout=30)
            if response.status_code != 200:
                logger.warning(f"Agent version {agent_version} not found for platform {platform} at {agent_url} (HTTP {response.status_code})")
                return False
            logger.debug(f"Agent version {agent_version} validated for platform {platform}")
        except requests.RequestException as e:
            logger.warning(f"Failed to validate agent version {agent_version} for platform {platform}: {e}")
            return False

    logger.info(f"Agent version {agent_version} validated for all platforms: {platforms}")
    return True


def validate_tools_version_exists(tools_version: str, platforms: List[str]) -> bool:
    """
    Validate that the tools version exists for all specified platforms by checking URLs.

    Args:
        tools_version: MongoDB tools version to validate
        platforms: List of platforms to check

    Returns:
        True if tools version exists for all platforms, False otherwise
    """
    agent_info = load_agent_build_info()
    tools_base_url = "https://fastdl.mongodb.org/tools/db"

    for platform in platforms:
        if platform not in agent_info["platform_mappings"]:
            logger.warning(f"Platform {platform} not found in agent mappings, skipping tools validation")
            continue

        mapping = agent_info["platform_mappings"][platform]
        tools_suffix = mapping["tools_suffix"].replace("{TOOLS_VERSION}", tools_version)
        tools_filename = f"{agent_info['base_names']['tools']}-{tools_suffix}"
        tools_url = f"{tools_base_url}/{tools_filename}"

        try:
            # Use HEAD request to check if URL exists without downloading the file
            response = requests.head(tools_url, timeout=30)
            if response.status_code != 200:
                logger.warning(f"Tools version {tools_version} not found for platform {platform} at {tools_url} (HTTP {response.status_code})")
                return False
            logger.debug(f"Tools version {tools_version} validated for platform {platform}")
        except requests.RequestException as e:
            logger.warning(f"Failed to validate tools version {tools_version} for platform {platform}: {e}")
            return False

    logger.info(f"Tools version {tools_version} validated for all platforms: {platforms}")
    return True

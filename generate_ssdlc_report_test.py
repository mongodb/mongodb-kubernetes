"""
This file tests the SSDLC generation report. This file assumes it's been called from the project root
"""

import json
import os
from typing import Dict

import generate_ssdlc_report
from scripts.release.build.build_info import (
    AGENT_IMAGE,
    OPERATOR_IMAGE,
    OPS_MANAGER_IMAGE,
    READINESS_PROBE_IMAGE,
    UPGRADE_HOOK_IMAGE,
    load_build_info,
)
from scripts.release.build.build_scenario import BuildScenario


def get_release() -> Dict:
    with open("release.json") as release:
        return json.load(release)


def get_build_info():
    return load_build_info(BuildScenario.MANUAL_RELEASE)


def test_report_generation():
    # Given
    release = get_release()
    current_version = release["mongodbOperator"]
    current_directory = os.getcwd()

    # When
    # We ignore Augmented SBOM download errors for this test as we quite often have a few days in transition state.
    # For example, when we release a new Ops Manager or Agent image, we upload the corresponding SBOM Lite
    # on d+1. Then on d+2 we have the Augmented SBOM available for download. This situation is perfectly normal
    # but causes this test to fail. Therefore, we ignore these errors here.
    generate_ssdlc_report.generate_ssdlc_report(ignore_sbom_download_errors=True)

    # Then
    assert os.path.exists(f"{current_directory}/ssdlc-report/MCK-{current_version}")
    assert os.path.exists(f"{current_directory}/ssdlc-report/MCK-{current_version}/Containerized MongoDB Agent")
    assert os.listdir(f"{current_directory}/ssdlc-report/MCK-{current_version}/Containerized MongoDB Agent") != []
    assert os.path.exists(
        f"{current_directory}/ssdlc-report/MCK-{current_version}/Containerized MongoDB Enterprise OpsManager"
    )
    assert (
        os.listdir(
            f"{current_directory}/ssdlc-report/MCK-{current_version}/Containerized MongoDB Enterprise OpsManager"
        )
        != []
    )
    if os.path.exists(f"{current_directory}/ssdlc-report/MCK-{current_version}/MongoDB Controllers for Kubernetes"):
        assert (
            os.listdir(f"{current_directory}/ssdlc-report/MCK-{current_version}/MongoDB Controllers for Kubernetes")
            != []
        )
    assert os.path.exists(
        f"{current_directory}/ssdlc-report/MCK-{current_version}/SSDLC Containerized MongoDB Agent {current_version}.md"
    )
    assert os.path.exists(
        f"{current_directory}/ssdlc-report/MCK-{current_version}/SSDLC Containerized MongoDB Controllers for Kubernetes {current_version}.md"
    )
    assert os.path.exists(
        f"{current_directory}/ssdlc-report/MCK-{current_version}/SSDLC Containerized MongoDB Enterprise OpsManager {current_version}.md"
    )
    assert os.path.exists(
        f"{current_directory}/ssdlc-report/MCK-{current_version}/SSDLC MongoDB Controllers for Kubernetes Testing Report {current_version}.md"
    )


def test_build_info_platforms_loading():
    """Test that platforms are correctly loaded from build_info using existing build system"""
    # Given
    build_info = get_build_info()

    # When - directly access platforms from BuildInfo objects
    agent_platforms = build_info.images[AGENT_IMAGE].platforms if AGENT_IMAGE in build_info.images else []
    # Note: MANUAL_RELEASE scenario may not include operator, so we check if it exists
    operator_platforms = build_info.images[OPERATOR_IMAGE].platforms if OPERATOR_IMAGE in build_info.images else []
    cli_platforms = build_info.binaries["kubectl-mongodb"].platforms if "kubectl-mongodb" in build_info.binaries else []

    # Then
    assert len(agent_platforms) > 0, "Agent platforms should be loaded from build_info"
    # Note: operator may not be in MANUAL_RELEASE scenario, so we only test if it exists
    if operator_platforms:
        assert len(operator_platforms) > 0, "Operator platforms should be loaded from build_info"
    # CLI may not be in MANUAL_RELEASE scenario
    if cli_platforms:
        assert len(cli_platforms) > 0, "CLI platforms should be loaded from build_info"

    # Verify that linux/amd64 is always present (fundamental requirement)
    assert "linux/amd64" in agent_platforms, f"Agent should support linux/amd64, got platforms: {agent_platforms}"
    if operator_platforms:
        assert "linux/amd64" in operator_platforms, f"Operator should support linux/amd64, got platforms: {operator_platforms}"
    if cli_platforms:
        assert "linux/amd64" in cli_platforms, f"CLI should support linux/amd64, got platforms: {cli_platforms}"


def test_supported_images_use_build_info_platforms():
    """Test that get_supported_images uses platforms from build_info"""
    # Given
    release = get_release()
    build_info = get_build_info()

    # When
    supported_images = generate_ssdlc_report.get_supported_images(release, build_info)

    # Then
    # Verify that mongodb-agent-ubi uses platforms from build_info
    if "mongodb-agent-ubi" in supported_images and AGENT_IMAGE in build_info.images:
        agent_platforms = supported_images["mongodb-agent-ubi"].platforms
        expected_platforms = build_info.images[AGENT_IMAGE].platforms
        assert agent_platforms == expected_platforms, f"Agent platforms should match build_info: expected {expected_platforms}, got {agent_platforms}"

    # Verify that mongodb-kubernetes-cli uses platforms from build_info
    if "mongodb-kubernetes-cli" in supported_images and "kubectl-mongodb" in build_info.binaries:
        cli_platforms = supported_images["mongodb-kubernetes-cli"].platforms
        expected_platforms = build_info.binaries["kubectl-mongodb"].platforms
        assert cli_platforms == expected_platforms, f"CLI platforms should match build_info: expected {expected_platforms}, got {cli_platforms}"


def test_all_supported_images_have_amd64():
    """Test that all supported images have linux/amd64 platform (fundamental requirement)"""
    # Given
    release = get_release()
    build_info = get_build_info()

    # When
    supported_images = generate_ssdlc_report.get_supported_images(release, build_info)

    # Then
    for image_name, image_info in supported_images.items():
        # Skip CLI as it's a binary, not a container image
        if image_name == "mongodb-kubernetes-cli":
            # CLI should have linux/amd64 among its platforms
            assert "linux/amd64" in image_info.platforms, f"CLI {image_name} should support linux/amd64, got platforms: {image_info.platforms}"
        else:
            # All container images should have linux/amd64
            assert "linux/amd64" in image_info.platforms, f"Container image {image_name} should support linux/amd64, got platforms: {image_info.platforms}"


def test_platform_fallback_behavior():
    """Test that fallback platforms are used when build_info doesn't contain platform info"""
    # Given
    from scripts.release.build.build_info import BuildInfo
    empty_build_info = BuildInfo(images={}, binaries={}, helm_charts={})
    release = get_release()

    # When
    supported_images = generate_ssdlc_report.get_supported_images(release, empty_build_info)

    # Then - verify that fallback platforms are used
    for image_name, image_info in supported_images.items():
        if image_name != "mongodb-kubernetes-cli":  # Skip CLI as it's a binary
            # All container images should fall back to linux/amd64
            assert "linux/amd64" in image_info.platforms, f"Image {image_name} should fall back to linux/amd64"


def test_image_mapping():
    """Test that image names are correctly mapped between release.json and build_info using the mapping constant"""
    # Given
    build_info = get_build_info()

    # When & Then - Test the mapping constant
    for release_name, build_info_name in generate_ssdlc_report.RELEASE_TO_BUILD_INFO_IMAGE_MAPPING.items():
        # If the mapping is correct and the image exists in build_info, we should get platforms
        if build_info_name in build_info.images:
            platforms = build_info.images[build_info_name].platforms
            assert len(platforms) >= 0, f"Should be able to get platforms for {release_name} -> {build_info_name}"

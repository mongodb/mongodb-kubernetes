"""
This file tests the SSDLC generation report. This file assumes it's been called from the project root
"""

import json
import os
from typing import Dict

import generate_ssdlc_report


def get_release() -> Dict:
    with open("release.json") as release:
        return json.load(release)


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

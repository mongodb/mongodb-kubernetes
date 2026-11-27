"""Regression tests for release bundle readiness."""

from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[3]
RELEASE_CONFIG = ROOT / ".evergreen-release-publish.yml"


def test_bundle_upload_task_is_a_static_dependency_of_rh_publish():
    config = yaml.safe_load(RELEASE_CONFIG.read_text())
    tasks = {task["name"]: task for task in config["tasks"]}
    variants = {variant["name"]: variant for variant in config["buildvariants"]}

    assert variants["prepare_openshift_bundles"]["tasks"] == [{"name": "prepare_and_upload_openshift_bundles"}]
    assert variants["prepare_openshift_bundles"]["depends_on"]
    assert variants["prepare_openshift_bundles_to_rh"]["depends_on"] == [
        {"name": "prepare_and_upload_openshift_bundles", "variant": "prepare_openshift_bundles"}
    ]
    assert "ensure_openshift_bundle_ready.sh" in str(tasks["prepare_and_upload_openshift_bundles"])

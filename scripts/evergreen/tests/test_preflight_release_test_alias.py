"""Regression test for the manual release preflight patch alias."""

from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[3]
RELEASE_CONFIG = ROOT / ".evergreen-release-publish.yml"
PROJECT_CONFIG = ROOT / ".evergreen.yml"


def test_manual_preflight_alias_has_a_dedicated_patch_only_variant():
    project = yaml.safe_load(PROJECT_CONFIG.read_text())
    publish = yaml.safe_load(RELEASE_CONFIG.read_text())
    alias = next(alias for alias in project["patch_aliases"] if alias["alias"] == "preflight_release_test")
    variant = next(
        variant for variant in publish["buildvariants"] if variant["name"] == "preflight_release_images_patch_test"
    )

    assert alias["variant_tags"] == ["preflight_release_test"]
    assert variant["tags"] == ["preflight_release_test"]
    assert variant["run_on"] == ["rhel90-large"]
    assert variant["allowed_requesters"] == ["patch"]
    assert variant["expansions"] == {"preflight_submit": False}
    assert variant["tasks"] == [{"name": "preflight_release_operator_images_task_group"}]
    assert "depends_on" not in variant
    assert "release-publish" not in variant["tags"]

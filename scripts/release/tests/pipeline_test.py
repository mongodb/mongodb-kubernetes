import argparse

import pytest

from scripts.release.pipeline import (
    branch_latest_tag_for,
    ecr_repository_name,
    image_build_config_from_args,
    isolated_repository,
)


class TestBranchLatestTagFor:
    def test_master_has_no_branch_latest_tag(self):
        assert branch_latest_tag_for("master") is None

    def test_v1_branch(self):
        assert branch_latest_tag_for("release-v1") == "latest-release-v1"

    def test_v2_branch(self):
        assert branch_latest_tag_for("release-v2") == "latest-release-v2"

    def test_untracked_branch(self):
        assert branch_latest_tag_for("some-feature-branch") is None

    def test_none_branch(self):
        assert branch_latest_tag_for(None) is None

    def test_v1_test_branch(self):
        assert branch_latest_tag_for("release-v1-test") == "latest-release-v1-test"

    def test_v2_test_branch(self):
        assert branch_latest_tag_for("release-v2-test") == "latest-release-v2-test"

    def test_v_test_no_number(self):
        assert branch_latest_tag_for("v-test") is None

    def test_missing_v_prefix(self):
        assert branch_latest_tag_for("1-test") is None


ECR_DEV = "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev"


def _args(image: str, scenario: str = "patch", **overrides) -> argparse.Namespace:
    values = dict(
        image=image,
        build_scenario=scenario,
        version="108.0.27.9091-1" if image == "agent" else "8.0.27",
        registry=None,
        platform=None,
        sign=None,
        skip_if_exists=None,
        parallel=False,
        parallel_factor=0,
        architecture_suffix=None,
        agent_tools_version="100.13.0" if image == "agent" else None,
        custom_agent_url="",
        isolated_namespace="",
    )
    values.update(overrides)
    return argparse.Namespace(**values)


class TestIsolatedNamespace:
    def test_isolated_repository(self):
        assert isolated_repository(f"{ECR_DEV}/mongodb-agent", "exp-123") == f"{ECR_DEV}/exp-123/mongodb-agent"

    def test_ecr_repository_name(self):
        assert ecr_repository_name(f"{ECR_DEV}/exp-1/mongodb-agent") == "dev/exp-1/mongodb-agent"
        assert ecr_repository_name("quay.io/mongodb/mongodb-agent") is None

    @pytest.mark.parametrize("image", ["agent", "ops-manager"])
    def test_default_behavior_unchanged(self, image):
        config = image_build_config_from_args(_args(image))
        assert config.registries == [
            f"{ECR_DEV}/{'mongodb-agent' if image == 'agent' else 'mongodb-enterprise-ops-manager-ubi'}"
        ]
        assert config.skip_if_exists is True

    @pytest.mark.parametrize(
        "image,repo", [("agent", "mongodb-agent"), ("ops-manager", "mongodb-enterprise-ops-manager-ubi")]
    )
    def test_isolated_namespace_overrides_registry_and_skip(self, image, repo):
        config = image_build_config_from_args(_args(image, isolated_namespace="exp-abc"))
        assert config.registries == [f"{ECR_DEV}/exp-abc/{repo}"]
        assert config.skip_if_exists is False
        assert config.sign is False
        assert config.latest_tag is False
        assert config.olm_tag is False
        assert config.branch_latest_tag is None

    def test_isolated_namespace_wins_over_explicit_skip_if_exists(self):
        config = image_build_config_from_args(_args("agent", isolated_namespace="exp-abc", skip_if_exists=True))
        assert config.skip_if_exists is False

    def test_isolated_namespace_allowed_in_development(self):
        config = image_build_config_from_args(_args("ops-manager", scenario="development", isolated_namespace="mine"))
        assert config.registries == [f"{ECR_DEV}/mine/mongodb-enterprise-ops-manager-ubi"]

    @pytest.mark.parametrize("scenario", ["staging", "release", "dryrun-release"])
    def test_isolated_namespace_rejected_outside_patch(self, scenario):
        with pytest.raises(ValueError, match="only allowed"):
            image_build_config_from_args(_args("agent", scenario=scenario, isolated_namespace="exp-abc"))

    @pytest.mark.parametrize("namespace", ["Exp", "exp/abc", "-exp", "exp-", "exp abc"])
    def test_invalid_namespace(self, namespace):
        with pytest.raises(ValueError, match="Invalid isolated namespace"):
            image_build_config_from_args(_args("agent", isolated_namespace=namespace))

    def test_registry_and_isolated_namespace_are_exclusive(self):
        with pytest.raises(ValueError, match="mutually exclusive"):
            image_build_config_from_args(_args("agent", isolated_namespace="exp-abc", registry="example.com/foo"))

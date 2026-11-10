import argparse
from unittest.mock import patch

import pytest

from scripts.release.atomic_pipeline import build_om_image
from scripts.release.build.build_scenario import BuildScenario
from scripts.release.build.image_build_configuration import ImageBuildConfiguration
from scripts.release.pipeline import image_build_config_from_args

OM_VERSION = "9.0.1"
CUSTOM_URL = "https://example.com/mongodb-mms-custom.tar.gz"
RELEASE_URL = "https://downloads.example.com/mongodb-mms-9.0.1.tar.gz"


def _args(om_download_url: str) -> argparse.Namespace:
    return argparse.Namespace(
        image="ops-manager",
        build_scenario="patch",
        version=OM_VERSION,
        registry=None,
        platform=None,
        sign=None,
        skip_if_exists=None,
        architecture_suffix=None,
        parallel=False,
        parallel_factor=0,
        agent_tools_version=None,
        custom_agent_url="",
        om_download_url=om_download_url,
    )


def _build_configuration(om_download_url: str = None) -> ImageBuildConfiguration:
    return ImageBuildConfiguration(
        scenario=BuildScenario.PATCH,
        version=OM_VERSION,
        latest_tag=False,
        olm_tag=False,
        registries=["268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-ops-manager-ubi"],
        dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile",
        builder=None,
        om_download_url=om_download_url,
    )


@pytest.mark.parametrize("om_download_url", [CUSTOM_URL, ""])
def test_om_download_url_from_args(om_download_url):
    assert image_build_config_from_args(_args(om_download_url)).om_download_url == om_download_url


@pytest.mark.parametrize("om_download_url, expected_url", [(CUSTOM_URL, CUSTOM_URL), (None, RELEASE_URL)])
@patch("scripts.release.atomic_pipeline.find_om_url", return_value=RELEASE_URL)
@patch("scripts.release.atomic_pipeline.build_image")
def test_build_om_image_download_url(mock_build_image, mock_find_om_url, om_download_url, expected_url):
    build_om_image(_build_configuration(om_download_url))

    assert mock_build_image.call_args.kwargs["build_args"] == {"version": OM_VERSION, "om_download_url": expected_url}
    assert mock_find_om_url.called == (om_download_url is None)

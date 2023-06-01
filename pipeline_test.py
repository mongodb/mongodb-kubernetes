from unittest import mock
from pipeline import operator_build_configuration


@mock.patch("builtins.open")
def test_operator_build_configuration(mock_open):
    config0 = """
export IMAGE_TYPE=ubi
export BASE_REPO_URL=somerepo/url
export NAMESPACE="something"
"""

    with mock.patch("builtins.open", mock.mock_open(read_data=config0), create=True):
        config = operator_build_configuration("builder", True)

    assert config.image_type == "ubi"
    assert config.base_repository == "somerepo/url"
    assert config.namespace == "something"

    with mock.patch("builtins.open", mock.mock_open(read_data=""), create=True):
        config = operator_build_configuration("builder", True)

    assert config.image_type == "ubuntu"
    assert config.base_repository == ""
    assert config.namespace == "default"


def test_calculate_skip_tags():
    config0 = """
export IMAGE_TYPE=ubi
export BASE_REPO_URL=somerepo/url
export NAMESPACE="something"
"""

    with mock.patch("builtins.open", mock.mock_open(read_data=config0), create=True):
        config = operator_build_configuration("builder", True)

    assert config.skip_tags() == ["ubuntu"]

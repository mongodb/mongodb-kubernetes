from unittest import mock

from pipeline import is_version_in_range, operator_build_configuration


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


def test_is_version_in_range():
    # When one bound is empty or None, always return True
    assert is_version_in_range("7.0.0", min_version="8.0.0", max_version="")
    assert is_version_in_range("7.0.0", min_version="8.0.0", max_version=None)
    assert is_version_in_range("9.0.0", min_version="", max_version="8.0.0")

    # Upper bound is excluded
    assert not is_version_in_range("8.1.1", min_version="8.0.0", max_version="8.1.1")

    # Lower bound is included
    assert is_version_in_range("8.0.0", min_version="8.0.0", max_version="8.1.1")

    # Test some values
    assert is_version_in_range("8.5.2", min_version="8.5.1", max_version="8.5.3")
    assert not is_version_in_range("8.5.2", min_version="8.5.3", max_version="8.4.2")

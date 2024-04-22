import os
from unittest import mock
from unittest.mock import patch

import pytest

from pipeline import (
    calculate_images_to_build,
    is_release_step_executed,
    is_version_in_range,
    operator_build_configuration,
)


def test_operator_build_configuration():
    with patch.dict(os.environ, {"distro": "a_distro", "BASE_REPO_URL": "somerepo/url", "namespace": "something"}):
        config = operator_build_configuration("builder", True, False)
        assert config.image_type == "a_distro"
        assert config.base_repository == "somerepo/url"
        assert config.namespace == "something"


def test_operator_build_configuration_defaults():
    with patch.dict(
        os.environ,
        {
            "BASE_REPO_URL": "",
        },
    ):
        config = operator_build_configuration("builder", True, False)
        assert config.image_type == "ubi"
        assert config.base_repository == ""
        assert config.namespace == "default"


@pytest.mark.parametrize(
    "test_case",
    [
        (["a", "b", "c"], ["a"], ["b"], {"a", "c"}),
        (["a", "b", "c"], ["a", "b"], None, {"a", "b"}),
        (["a", "b", "c"], None, ["a"], {"b", "c"}),
        (["a", "b", "c"], [], [], {"a", "b", "c"}),
        (["a", "b", "c"], ["d"], None, ValueError),
        (["a", "b", "c"], None, ["d"], ValueError),
        ([], ["a"], ["b"], ValueError),
        (["a", "b", "c"], None, None, {"a", "b", "c"}),
    ],
)
def test_calculate_images_to_build(test_case):
    images, include, exclude, expected = test_case
    if expected is ValueError:
        with pytest.raises(ValueError):
            calculate_images_to_build(images, include, exclude)
    else:
        assert calculate_images_to_build(images, include, exclude) == expected


@pytest.mark.parametrize(
    "version,min_version,max_version,expected",
    [
        # When one bound is empty or None, always return True
        ("7.0.0", "8.0.0", "", True),
        ("7.0.0", "8.0.0", None, True),
        ("9.0.0", "", "8.0.0", True),
        # Upper bound is excluded
        ("8.1.1", "8.0.0", "8.1.1", False),
        # Lower bound is included
        ("8.0.0", "8.0.0", "8.1.1", True),
        # Test some values
        ("8.5.2", "8.5.1", "8.5.3", True),
        ("8.5.2", "8.5.3", "8.4.2", False),
    ],
)
def test_is_version_in_range(version, min_version, max_version, expected):
    assert is_version_in_range(version, min_version, max_version) == expected


@pytest.mark.parametrize(
    "description,case",
    [
        ("No skip or include tags", {"skip_tags": [], "include_tags": [], "expected": True}),
        ("Include 'release' only", {"skip_tags": [], "include_tags": ["release"], "expected": True}),
        ("Skip 'release' only", {"skip_tags": ["release"], "include_tags": [], "expected": False}),
        ("Include non-release, no skip", {"skip_tags": [], "include_tags": ["test", "deploy"], "expected": False}),
        ("Skip non-release, no include", {"skip_tags": ["test", "deploy"], "include_tags": [], "expected": True}),
        ("Include and skip 'release'", {"skip_tags": ["release"], "include_tags": ["release"], "expected": False}),
        (
            "Skip non-release, include 'release'",
            {"skip_tags": ["test", "deploy"], "include_tags": ["release"], "expected": True},
        ),
    ],
)
def test_is_release_step_executed(description, case):
    result = is_release_step_executed(case["skip_tags"], case["include_tags"])  # Unpack arguments by their names
    assert result == case["expected"], f"Test failed: {description}. Expected {case['expected']}, got {result}."

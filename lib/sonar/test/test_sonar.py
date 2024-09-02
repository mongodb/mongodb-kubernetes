import logging
from unittest.mock import Mock, call, patch

import pytest

from ..sonar import (
    SonarAPIError,
    create_ecr_repository,
    is_valid_ecr_repo,
    process_image,
)


@patch("sonar.sonar.find_inventory", return_value={"images": {"name": "image-name"}})
@patch("sonar.sonar.find_image", return_value={"name": "image-name"})
def test_specific_inventory(patched_find_image, patched_find_inventory):
    process_image(
        image_name="image-name",
        skip_tags=[],
        include_tags=[],
        build_args={},
        inventory="other-inventory.yaml",
    )

    patched_find_image.assert_called_once_with("image-name", "other-inventory.yaml")
    patched_find_inventory.assert_called_once_with("other-inventory.yaml")


def test_repo_is_not_ecr():
    repos = (
        "quay.io/some-org/some-repo",
        "scan.connect.redhat.com/ospid-10001000100-1000/some-repo",
        "docker.io/some-more",
        "1.dkr.ecr.us-east-1.amazonaws.com",  # needs bigger account number
        "1.dkr.ecr.us-east.amazonaws.com",  # zone is not defined
    )
    for repo in repos:
        assert is_valid_ecr_repo(repo) is False


def test_repo_is_ecr():
    repos = (
        "123456789012.dkr.ecr.eu-west-1.amazonaws.com/some-other-repo",
        "123456789012.dkr.ecr.us-east-1.amazonaws.com/something-else",
    )
    for repo in repos:
        assert is_valid_ecr_repo(repo)


@patch("sonar.sonar.boto3.client")
def test_create_ecr_repository_creates_repo_when_ecr_repo(patched_client: Mock):
    returned_client = Mock()
    patched_client.return_value = returned_client

    # repository with no tag
    create_ecr_repository(
        "123456789012.dkr.ecr.eu-west-1.amazonaws.com/some-other-repo",
    )
    patched_client.assert_called_once()
    returned_client.create_repository.assert_called_once_with(
        repositoryName="some-other-repo",
        imageTagMutability="MUTABLE",
        imageScanningConfiguration={"scanOnPush": False},
    )
    patched_client.reset_mock()

    # repository with a tag
    create_ecr_repository(
        "123456789012.dkr.ecr.eu-west-1.amazonaws.com/some-other-repo:some-tag",
    )
    patched_client.assert_called_once()
    returned_client.create_repository.assert_called_once_with(
        repositoryName="some-other-repo",
        imageTagMutability="MUTABLE",
        imageScanningConfiguration={"scanOnPush": False},
    )


@patch("sonar.sonar.boto3.client")
def test_create_ecr_repository_doesnt_create_repo_when_not_ecr_repo(
    patched_client: Mock,
):
    returned_client = Mock()
    patched_client.return_value = returned_client

    create_ecr_repository(
        "my-private-repo.com/something",
    )
    patched_client.assert_not_called()


@patch("sonar.sonar.docker_push")
@patch("sonar.sonar.docker_tag")
@patch("sonar.sonar.docker_build")
def test_continue_on_errors(_docker_build, _docker_tag, mocked_docker_push):
    """We'll mock a function that fails on first iteration but succeeds the seconds one."""
    mocked_docker_push.return_value = None
    mocked_docker_push.side_effect = ["All ok!", SonarAPIError("fake-error"), "All ok!"]

    pipeline = process_image(
        image_name="image1",
        skip_tags=[],
        include_tags=["test_continue_on_errors"],
        build_args={},
        build_options={"pipeline": True, "continue_on_errors": True},
        inventory="lib/sonar/test/yaml_scenario6.yaml",
    )

    # Assert docker_push was called three times, even if one of them failed
    assert mocked_docker_push.call_count == 3


@patch("sonar.sonar.docker_push")
@patch("sonar.sonar.docker_tag")
@patch("sonar.sonar.docker_build")
def test_do_not_continue_on_errors(_docker_build, _docker_tag, mocked_docker_push):
    mocked_docker_push.return_value = None
    mocked_docker_push.side_effect = [
        SonarAPIError("fake-error-should-not-continue"),
        "All ok!",
    ]

    with pytest.raises(SonarAPIError):
        pipeline = process_image(
            image_name="image1",
            skip_tags=[],
            include_tags=["test_continue_on_errors"],
            build_args={},
            build_options={
                "pipeline": True,
                "continue_on_errors": False,
            },
            inventory="lib/sonar/test/yaml_scenario6.yaml",
        )

    # docker_push raised first time, only one call expected
    assert mocked_docker_push.call_count == 1


@patch("sonar.sonar.docker_push")
@patch("sonar.sonar.docker_tag")
@patch("sonar.sonar.docker_build")
def test_fail_on_captured_errors(_docker_build, _docker_tag, mocked_docker_push):
    mocked_docker_push.return_value = None
    mocked_docker_push.side_effect = [
        "All ok!",
        SonarAPIError("fake-error-should-not-continue"),
        "All ok!",
    ]

    with pytest.raises(SonarAPIError):
        pipeline = process_image(
            image_name="image1",
            skip_tags=[],
            include_tags=["test_continue_on_errors"],
            build_args={},
            build_options={
                "pipeline": True,
                "continue_on_errors": True,
                "fail_on_errors": True,
            },
            inventory="lib/sonar/test/yaml_scenario6.yaml",
        )

    # docker_push raised second time time, but allowed to continue,
    # anyway, process_image still raised at the end!
    assert mocked_docker_push.call_count == 3

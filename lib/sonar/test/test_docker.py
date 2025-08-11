from types import SimpleNamespace
from unittest.mock import Mock, call, patch

import pytest
from pytest_mock import MockerFixture

from ..builders import SonarAPIError
from ..builders.docker import docker_push


@patch("sonar.builders.docker.shutil.which", return_value="/mock/path/to/docker")
def test_docker_push_is_retried(mock_which, mocker: MockerFixture):
    a = SimpleNamespace(returncode=1, stderr="some-error")
    sp = mocker.patch("sonar.builders.docker.subprocess")
    sp.PIPE = "|PIPE|"
    sp.run.return_value = a

    with pytest.raises(SonarAPIError, match="some-error"):
        docker_push("reg", "tag")

    # docker push is called 4 times, the last time it is called, it raises an exception
    sp.run.assert_has_calls(
        [
            call(["/mock/path/to/docker", "push", "reg:tag"], stdout="|PIPE|", stderr="|PIPE|"),
            call(["/mock/path/to/docker", "push", "reg:tag"], stdout="|PIPE|", stderr="|PIPE|"),
            call(["/mock/path/to/docker", "push", "reg:tag"], stdout="|PIPE|", stderr="|PIPE|"),
            call(["/mock/path/to/docker", "push", "reg:tag"], stdout="|PIPE|", stderr="|PIPE|"),
        ]
    )


@patch("sonar.builders.docker.shutil.which", return_value="/mock/path/to/docker")
def test_docker_push_is_retried_and_works(mock_which, mocker: MockerFixture):
    ok = SimpleNamespace(returncode=0)
    sp = mocker.patch("sonar.builders.docker.subprocess")
    sp.PIPE = "|PIPE|"
    sp.run = Mock()
    sp.run.return_value = ok

    docker_push("reg", "tag")

    sp.run.assert_called_once_with(
        ["/mock/path/to/docker", "push", "reg:tag"],
        stdout="|PIPE|",
        stderr="|PIPE|",
    )

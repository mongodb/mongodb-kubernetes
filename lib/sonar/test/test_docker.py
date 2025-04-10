from types import SimpleNamespace
from unittest.mock import Mock, call

import pytest
from pytest_mock import MockerFixture

from ..builders import SonarAPIError
from ..builders.docker import docker_push


def test_docker_push_is_retried(mocker: MockerFixture):
    a = SimpleNamespace(returncode=1, stderr="some-error")
    sp = mocker.patch("sonar.builders.docker.subprocess")
    sp.PIPE = "|PIPE|"
    sp.run.return_value = a

    with pytest.raises(SonarAPIError, match="some-error"):
        docker_push("reg", "tag")

    # docker push is called 4 times, the last time it is called, it raises an exception
    sp.run.assert_has_calls(
        [
            call(["docker", "push", "reg:tag"], stdout="|PIPE|", stderr="|PIPE|"),
            call(["docker", "push", "reg:tag"], stdout="|PIPE|", stderr="|PIPE|"),
            call(["docker", "push", "reg:tag"], stdout="|PIPE|", stderr="|PIPE|"),
            call(["docker", "push", "reg:tag"], stdout="|PIPE|", stderr="|PIPE|"),
        ]
    )


def test_docker_push_is_retried_and_works(mocker: MockerFixture):

    ok = SimpleNamespace(returncode=0)
    sp = mocker.patch("sonar.builders.docker.subprocess")
    sp.PIPE = "|PIPE|"
    sp.run = Mock()
    sp.run.return_value = ok

    docker_push("reg", "tag")

    sp.run.assert_called_once_with(
        ["docker", "push", "reg:tag"],
        stdout="|PIPE|",
        stderr="|PIPE|",
    )

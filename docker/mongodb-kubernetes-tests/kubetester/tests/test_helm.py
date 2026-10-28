import subprocess
from unittest.mock import MagicMock

import pytest
from kubetester import helm

TRANSIENT_STDERR = b"Error: context deadline exceeded (Client.Timeout or context cancellation while reading body)"
PERMANENT_STDERR = b"Error: chart version not found"
ALREADY_EXISTS_STDERR = b"Error: cannot re-use a name: release: already exists"


def helm_failure(stderr: bytes) -> subprocess.CalledProcessError:
    return subprocess.CalledProcessError(1, "helm", stderr=stderr)


@pytest.mark.parametrize(
    "side_effects,expected_calls,expected_error",
    [
        pytest.param([None], 1, False, id="succeeds on first attempt"),
        pytest.param([helm_failure(TRANSIENT_STDERR), None], 2, False, id="retries a transient error"),
        pytest.param([helm_failure(TRANSIENT_STDERR)] * 3, 3, True, id="gives up after all attempts"),
        pytest.param([helm_failure(PERMANENT_STDERR)], 1, True, id="does not retry a permanent error"),
        pytest.param([helm_failure(ALREADY_EXISTS_STDERR)], 1, False, id="tolerates an existing release"),
    ],
)
def test_helm_install_from_chart_retries(monkeypatch, side_effects, expected_calls, expected_error):
    run = MagicMock(side_effect=side_effects)
    monkeypatch.setattr(helm, "process_run_and_check", run)
    monkeypatch.setattr(helm, "helm_repo_add", MagicMock())
    monkeypatch.setattr(helm.time, "sleep", MagicMock())

    if expected_error:
        with pytest.raises(subprocess.CalledProcessError):
            helm.helm_install_from_chart("ns", "release", "repo/chart")
    else:
        helm.helm_install_from_chart("ns", "release", "repo/chart")

    assert run.call_count == expected_calls

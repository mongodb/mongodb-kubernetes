import subprocess
from unittest.mock import MagicMock

import pytest
from kubetester import helm

TRANSIENT_STDERR = b"Error: context deadline exceeded (Client.Timeout or context cancellation while reading body)"
REPO_UNREACHABLE_STDERR = (
    b'Error: looks like "https://charts.jetstack.io" is not a valid chart repository or cannot be reached: '
    b"context deadline exceeded (Client.Timeout or context cancellation while reading body)"
)
PERMANENT_STDERR = b"Error: chart version not found"
ALREADY_EXISTS_STDERR = b"Error: cannot re-use a name: release: already exists"


def helm_failure(stderr: bytes) -> subprocess.CalledProcessError:
    return subprocess.CalledProcessError(1, "helm", stderr=stderr)


@pytest.mark.parametrize(
    "repo_add_effects,install_effects,expected_repo_add_calls,expected_install_calls,expected_error",
    [
        pytest.param([None], [None], 1, 1, False, id="succeeds on first attempt"),
        pytest.param(
            [None, None], [helm_failure(TRANSIENT_STDERR), None], 2, 2, False, id="retries a transient install"
        ),
        pytest.param(
            [helm_failure(REPO_UNREACHABLE_STDERR), None], [None], 2, 1, False, id="retries an unreachable repo"
        ),
        pytest.param([None] * 3, [helm_failure(TRANSIENT_STDERR)] * 3, 3, 3, True, id="gives up on install"),
        pytest.param([helm_failure(REPO_UNREACHABLE_STDERR)] * 3, [], 3, 0, True, id="gives up on repo add"),
        pytest.param([None], [helm_failure(PERMANENT_STDERR)], 1, 1, True, id="does not retry a permanent error"),
        pytest.param([None], [helm_failure(ALREADY_EXISTS_STDERR)], 1, 1, False, id="tolerates an existing release"),
    ],
)
def test_helm_install_from_chart_retries(
    monkeypatch, repo_add_effects, install_effects, expected_repo_add_calls, expected_install_calls, expected_error
):
    repo_add = MagicMock(side_effect=repo_add_effects)
    install = MagicMock(side_effect=install_effects)
    monkeypatch.setattr(helm, "helm_repo_add", repo_add)
    monkeypatch.setattr(helm, "process_run_and_check", install)
    monkeypatch.setattr(helm.time, "sleep", MagicMock())

    if expected_error:
        with pytest.raises(subprocess.CalledProcessError):
            helm.helm_install_from_chart("ns", "release", "repo/chart")
    else:
        helm.helm_install_from_chart("ns", "release", "repo/chart")

    assert repo_add.call_count == expected_repo_add_calls
    assert install.call_count == expected_install_calls

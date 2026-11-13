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
REPO_NOT_FOUND_STDERR = b"Error: 404 Not Found"
ALREADY_EXISTS_STDERR = b"Error: cannot re-use a name: release: already exists"


def test_helm_template_rejects_unversioned_oci_chart(monkeypatch):
    monkeypatch.delenv(helm.OCI_HELM_VERSION_ENV_VAR_NAME, raising=False)
    execute = MagicMock()
    monkeypatch.setattr(helm, "process_run_and_check", execute)
    with pytest.raises(ValueError, match="exact chart version"):
        helm.helm_template({}, helm_chart_path="oci://registry/chart")
    execute.assert_not_called()


def helm_failure(stderr: bytes) -> subprocess.CalledProcessError:
    return subprocess.CalledProcessError(1, "helm", stderr=stderr)


@pytest.mark.parametrize(
    "install_effects,expected_install_calls,expected_error",
    [
        pytest.param([None], 1, False, id="succeeds on first attempt"),
        pytest.param([helm_failure(TRANSIENT_STDERR), None], 2, False, id="retries a transient install"),
        pytest.param([helm_failure(TRANSIENT_STDERR)] * 3, 3, True, id="gives up on install"),
        pytest.param([helm_failure(PERMANENT_STDERR)], 1, True, id="does not retry a permanent error"),
        pytest.param([helm_failure(ALREADY_EXISTS_STDERR)], 1, False, id="tolerates an existing release"),
    ],
)
def test_helm_install_from_chart_retries(monkeypatch, install_effects, expected_install_calls, expected_error):
    repo_add = MagicMock()
    install = MagicMock(side_effect=install_effects)
    monkeypatch.setattr(helm, "helm_repo_add", repo_add)
    monkeypatch.setattr(helm, "process_run_and_check", install)
    monkeypatch.setattr(helm.time, "sleep", MagicMock())

    if expected_error:
        with pytest.raises(subprocess.CalledProcessError):
            helm.helm_install_from_chart("ns", "release", "repo/chart")
    else:
        helm.helm_install_from_chart("ns", "release", "repo/chart")

    assert repo_add.call_count == 1
    assert install.call_count == expected_install_calls


@pytest.mark.parametrize(
    "effects,expected_calls,expected_error",
    [
        pytest.param([None], 1, False, id="succeeds on first attempt"),
        pytest.param([helm_failure(REPO_UNREACHABLE_STDERR), None], 2, False, id="retries an unreachable repo"),
        pytest.param(
            [helm_failure(REPO_UNREACHABLE_STDERR)] * helm.HELM_REPO_ADD_MAX_ATTEMPTS,
            helm.HELM_REPO_ADD_MAX_ATTEMPTS,
            True,
            id="gives up after the max attempts",
        ),
        pytest.param([helm_failure(REPO_NOT_FOUND_STDERR)], 1, True, id="does not retry a permanent error"),
    ],
)
def test_helm_repo_add_retries(monkeypatch, effects, expected_calls, expected_error):
    run = MagicMock(side_effect=effects)
    monkeypatch.setattr(helm, "process_run_and_check", run)
    monkeypatch.setattr(helm.time, "sleep", MagicMock())

    if expected_error:
        with pytest.raises(subprocess.CalledProcessError):
            helm.helm_repo_add("jetstack", "https://charts.jetstack.io")
    else:
        helm.helm_repo_add("jetstack", "https://charts.jetstack.io")

    assert run.call_count == expected_calls

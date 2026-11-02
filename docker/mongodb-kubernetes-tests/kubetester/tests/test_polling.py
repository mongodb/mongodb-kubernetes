import pytest
import requests
from kubernetes.client.rest import ApiException
from kubetester import polling
from kubetester.mongodb_common import MongoDBCommon
from kubetester.polling import PollTimeoutError, poll_until
from urllib3.exceptions import ConnectTimeoutError, ReadTimeoutError


class FakeClock:
    def __init__(self):
        self.now = 0.0

    def monotonic(self):
        return self.now

    def sleep(self, seconds):
        self.now += seconds


@pytest.fixture
def clock(monkeypatch):
    clock = FakeClock()
    monkeypatch.setattr(polling.time, "monotonic", clock.monotonic)
    monkeypatch.setattr(polling.time, "sleep", clock.sleep)
    return clock


def test_polls_until_condition_is_ready(clock):
    observations = iter([(False, "members missing"), (False, "members missing"), (True, "members present")])

    result = poll_until(
        lambda deadline: next(observations),
        timeout=10,
        interval=2,
        description="MongoDB members",
    )

    assert result is True
    assert clock.now == 4


def test_timeout_counts_time_spent_observing_and_reports_last_state(clock):
    attempts = []

    def check(deadline):
        attempts.append(deadline.remaining())
        clock.now += 2
        return False, "members missing"

    with pytest.raises(PollTimeoutError, match="MongoDB members.*members missing"):
        poll_until(check, timeout=5, interval=2, description="MongoDB members")

    assert attempts == [5, 1]
    assert clock.now == 6


def test_retries_only_the_error_types_selected_by_the_caller(clock):
    class TemporaryReadError(Exception):
        pass

    observations = iter([TemporaryReadError("transient"), (True, "members present")])

    def check(deadline):
        observation = next(observations)
        if isinstance(observation, Exception):
            raise observation
        return observation

    assert poll_until(
        check,
        timeout=5,
        interval=1,
        description="MongoDB members",
        retry_on=(TemporaryReadError,),
    )
    assert clock.now == 1


def test_unexpected_error_is_not_retried(clock):
    def check(deadline):
        raise ValueError("invalid test condition")

    with pytest.raises(ValueError, match="invalid test condition"):
        poll_until(check, timeout=5, interval=1, description="MongoDB members")

    assert clock.now == 0


def test_rejects_unbounded_or_invalid_timeouts(clock):
    with pytest.raises(ValueError, match="timeout"):
        poll_until(lambda deadline: True, timeout=0, interval=1, description="cleanup")


@pytest.mark.parametrize("status", [429, 502, 503, 504])
def test_retryable_kubernetes_read_statuses(status):
    assert polling.is_transient_read_error(ApiException(status=status))


@pytest.mark.parametrize("status", [401, 403, 404, 409, 500])
def test_nonretryable_kubernetes_read_statuses(status):
    assert not polling.is_transient_read_error(ApiException(status=status))


@pytest.mark.parametrize(
    "error",
    [
        requests.exceptions.Timeout(),
        requests.exceptions.ConnectionError(),
        ConnectTimeoutError(),
        ReadTimeoutError(None, "/resource", "timeout"),
    ],
)
def test_retryable_network_read_errors(error):
    assert polling.is_transient_read_error(error)


@pytest.mark.parametrize("error", [requests.exceptions.SSLError(), requests.exceptions.ProxyError(), ValueError()])
def test_configuration_and_unexpected_errors_are_not_retryable(error):
    assert not polling.is_transient_read_error(error)


@pytest.mark.parametrize(("status", "expected"), [(401, False), (404, False), (429, True), (503, True)])
def test_ops_manager_http_errors_are_classified_by_status(status, expected):
    response = requests.Response()
    response.status_code = status
    error = requests.exceptions.HTTPError(response=response)
    assert polling.is_transient_read_error(error) is expected


def test_consecutive_successes_are_reset_after_transient_read_error(clock):
    class TemporaryReadError(Exception):
        pass

    observations = iter([True, TemporaryReadError(), True, True])

    def check(deadline):
        result = next(observations)
        if isinstance(result, Exception):
            raise result
        return result

    assert poll_until(
        check,
        timeout=5,
        interval=1,
        description="stable phase",
        retry_on=(TemporaryReadError,),
        persist_for=2,
    )
    assert clock.now == 3


def test_success_returned_after_deadline_is_not_accepted(clock):
    def check(deadline):
        clock.now += 3
        return True

    with pytest.raises(PollTimeoutError, match="cleanup"):
        poll_until(check, timeout=2, interval=1, description="cleanup")


def test_timeout_without_raise_returns_none_and_logs(clock, caplog):
    assert (
        poll_until(
            lambda deadline: (False, "still present"), timeout=2, interval=1, description="cleanup", should_raise=False
        )
        is None
    )
    assert "still present" in caplog.text


class FakeMongoDB(MongoDBCommon):
    def __init__(self, observations, cached_members):
        self.observations = iter(observations)
        self.members = cached_members
        self.request_timeouts = []

    def reload(self, *, request_timeout=None):
        self.request_timeouts.append(request_timeout)
        observation = next(self.observations)
        if isinstance(observation, Exception):
            raise observation
        self.members = observation


def test_shared_resource_wait_never_checks_cached_state_after_failed_reload(clock):
    resource = FakeMongoDB([requests.exceptions.Timeout(), None, 3], cached_members=3)
    observed_members = []

    def members_present(mdb):
        observed_members.append(mdb.members)
        return mdb.members is not None

    assert resource.wait_for(members_present, timeout=10, use_shared_poller=True, description="members")
    assert observed_members == [None, 3]
    assert resource.request_timeouts == [10, 7, 4]


def test_shared_resource_wait_fails_fast_on_forbidden_reload(clock):
    resource = FakeMongoDB([ApiException(status=403)], cached_members=3)
    observed_members = []

    with pytest.raises(ApiException) as error:
        resource.wait_for(
            lambda mdb: observed_members.append(mdb.members) or True,
            timeout=10,
            use_shared_poller=True,
            description="members",
        )

    assert error.value.status == 403
    assert observed_members == []

import ast
import json
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock
from unittest.mock import call as mock_call

import pytest
import requests
from kubernetes import client
from kubernetes.client.rest import ApiException, RESTClientObject
from kubetester import polling
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb_common import MongoDBCommon
from kubetester.polling import PollTimeoutError, poll_until
from urllib3.exceptions import ConnectTimeoutError, MaxRetryError, ReadTimeoutError
from urllib3.util.retry import Retry


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


@pytest.mark.parametrize(("status", "expected"), [(500, False), (502, True), (503, True), (504, True)])
def test_kubernetes_client_exhausted_status_retries_are_classified(status, expected):
    retry = Retry(total=0, status_forcelist=[500, 502, 503, 504])
    response = SimpleNamespace(status=status, get_redirect_location=lambda: None)

    with pytest.raises(MaxRetryError) as error:
        retry.increment(method="GET", url="/resource", response=response)

    assert polling.is_transient_read_error(error.value) is expected


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
    observations = iter([True, requests.exceptions.ConnectionError(), True, True])

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
        persist_for=2,
    )
    assert clock.now == 3


def test_success_that_overruns_the_deadline_is_accepted(clock):
    def check(deadline):
        clock.now += 3
        return True

    assert poll_until(check, timeout=2, interval=1, description="cleanup")


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


def test_wait_for_rejects_a_non_bool_condition(clock):
    resource = FakeMongoDB([None], cached_members=3)

    with pytest.raises(TypeError, match="must return a bool"):
        resource.wait_for(lambda mdb: "10.0.0.1", timeout=5, description="external ip")


def test_shared_resource_wait_never_checks_cached_state_after_failed_reload(clock):
    resource = FakeMongoDB([requests.exceptions.Timeout(), None, 3], cached_members=3)
    observed_members = []

    def members_present(mdb):
        observed_members.append(mdb.members)
        return mdb.members is not None

    assert resource.wait_for(members_present, timeout=10, description="members")
    assert observed_members == [None, 3]
    assert resource.request_timeouts == [10, 7, 4]


def test_shared_resource_wait_fails_fast_on_forbidden_reload(clock):
    resource = FakeMongoDB([ApiException(status=403)], cached_members=3)
    observed_members = []

    with pytest.raises(ApiException) as error:
        resource.wait_for(
            lambda mdb: observed_members.append(mdb.members) or True,
            timeout=10,
            description="members",
        )

    assert error.value.status == 403
    assert observed_members == []


@pytest.mark.parametrize("status", [401, 403, 429, 503])
def test_shared_deletion_does_not_treat_non_404_as_deleted(clock, monkeypatch, status):
    api = MagicMock()
    api.get_namespaced_custom_object.side_effect = ApiException(status=status)
    monkeypatch.setattr(KubernetesTester, "clients", lambda name: api)

    with pytest.raises(ApiException) as error:
        KubernetesTester.is_deleted("test", "database", deadline=polling.Deadline(10))

    assert error.value.status == status


def test_shared_deletion_only_accepts_kubernetes_404_and_bounds_the_read(clock, monkeypatch):
    rest = RESTClientObject(client.Configuration())
    rest.pool_manager.request = MagicMock(return_value=SimpleNamespace(status=200))

    def get_object(*args, _request_timeout):
        rest.request("GET", "https://example.invalid", _preload_content=False, _request_timeout=_request_timeout)
        raise ApiException(status=404)

    api = SimpleNamespace(get_namespaced_custom_object=get_object)
    monkeypatch.setattr(KubernetesTester, "clients", lambda name: api)
    clock.now = 7.5

    assert KubernetesTester.is_deleted("test", "database", deadline=polling.Deadline(10))
    timeout = rest.pool_manager.request.call_args.kwargs["timeout"]
    assert timeout.connect_timeout == 2.5
    assert timeout.read_timeout == 2.5


def test_legacy_deletion_keeps_its_existing_error_behavior(monkeypatch):
    api = MagicMock()
    api.get_namespaced_custom_object.side_effect = ApiException(status=403)
    monkeypatch.setattr(KubernetesTester, "clients", lambda name: api)

    assert KubernetesTester.is_deleted("test", "database")
    assert "_request_timeout" not in api.get_namespaced_custom_object.call_args.kwargs


def om_response(status, body):
    response = requests.Response()
    response.status_code = status
    response.url = "https://om.invalid"
    response._content = json.dumps(body).encode()
    return response


@pytest.fixture
def ops_manager_environment(monkeypatch):
    monkeypatch.setenv("NAMESPACE", "test")
    monkeypatch.setenv("OM_HOST", "https://om.invalid")
    monkeypatch.setenv("OM_USER", "user")
    monkeypatch.setenv("OM_API_KEY", "not-a-secret")
    monkeypatch.delenv("OM_ORGID", raising=False)
    monkeypatch.delenv("OM_PROJECT_ID", raising=False)


def test_shared_cleanup_bounds_uncached_group_discovery_and_all_reads(clock, monkeypatch, ops_manager_environment):
    monkeypatch.setattr(KubernetesTester, "group_id", None)
    monkeypatch.setattr(KubernetesTester, "namespace", "test", raising=False)
    monkeypatch.setattr(KubernetesTester, "name", "database", raising=False)
    monkeypatch.setattr(KubernetesTester, "kind", "MongoDB", raising=False)
    timeouts = []

    def get_object(*args, _request_timeout):
        assert _request_timeout == (20, 20)
        clock.now += 0.5
        raise ApiException(status=404)

    monkeypatch.setattr(
        KubernetesTester, "clients", lambda name: SimpleNamespace(get_namespaced_custom_object=get_object)
    )

    def request(method, url, *, timeout, **kwargs):
        assert method == "get"
        timeouts.append(timeout)
        clock.now += 0.5
        if "/orgs?" in url:
            page = int(url[-1])
            body = {
                "results": [{"id": "org", "name": "test"}] if page == 2 else [],
                "links": [{"rel": "next"}] if page == 1 else [],
            }
        elif "/orgs/org/groups?" in url:
            page = int(url[-1])
            body = {
                "results": [{"id": "group", "name": "test"}] if page == 2 else [],
                "links": [{"rel": "next"}] if page == 1 else [],
            }
        elif url.endswith("/groups/group"):
            body = {"id": "group"}
        elif url.endswith("/automationConfig"):
            body = {"replicaSets": [], "sharding": [], "processes": []}
        elif url.endswith("/hosts"):
            body = {"results": []}
        else:
            pytest.fail(f"unexpected Ops Manager URL: {url}")
        return om_response(200, body)

    monkeypatch.setattr(requests, "request", request)

    ready, observation = KubernetesTester.mongo_resource_deleted(deadline=polling.Deadline(20))

    assert ready is True
    assert observation == "Ops Manager state cleaned"
    assert timeouts == [19.5, 19, 18.5, 18, 17.5, 17, 16.5]


@pytest.mark.parametrize(
    ("body", "cleaned"),
    [
        ({"errorCode": "GROUP_NOT_FOUND"}, True),
        ({"errorCode": "NOT_FOUND"}, False),
        ({}, False),
        ([], False),
    ],
)
def test_shared_cleanup_accepts_only_confirmed_missing_group(
    clock, monkeypatch, ops_manager_environment, body, cleaned
):
    monkeypatch.setattr(KubernetesTester, "group_id", "group")
    monkeypatch.setattr(requests, "request", lambda *args, **kwargs: om_response(404, body))

    if cleaned:
        assert KubernetesTester.is_om_state_cleaned(deadline=polling.Deadline(10))
    else:
        with pytest.raises(requests.exceptions.HTTPError) as error:
            KubernetesTester.is_om_state_cleaned(deadline=polling.Deadline(10))
        assert error.value.response.status_code == 404


def test_shared_cleanup_exposes_transient_om_http_errors_to_poller(clock, monkeypatch, ops_manager_environment):
    monkeypatch.setattr(KubernetesTester, "group_id", "group")
    monkeypatch.setattr(requests, "request", lambda *args, **kwargs: om_response(503, {}))

    with pytest.raises(requests.exceptions.HTTPError) as error:
        KubernetesTester.is_om_state_cleaned(deadline=polling.Deadline(10))

    assert polling.is_transient_read_error(error.value)


def test_legacy_cleanup_keeps_its_existing_missing_group_behavior(monkeypatch, ops_manager_environment):
    monkeypatch.setattr(KubernetesTester, "group_id", "group")
    monkeypatch.setattr(requests, "request", lambda *args, **kwargs: om_response(404, {}))

    assert KubernetesTester.is_om_state_cleaned()


def test_shared_deletion_wait_recovers_from_transient_reads_and_waits_for_cleanup(
    clock, monkeypatch, ops_manager_environment
):
    monkeypatch.setattr(KubernetesTester, "group_id", "group")
    monkeypatch.setattr(KubernetesTester, "namespace", "test", raising=False)
    monkeypatch.setattr(KubernetesTester, "name", "database", raising=False)
    monkeypatch.setattr(KubernetesTester, "kind", "MongoDB", raising=False)
    kubernetes_statuses = iter([503, 404, 404, 404])
    configs = iter(
        [
            om_response(503, {}),
            om_response(200, {"replicaSets": ["still present"], "sharding": [], "processes": []}),
            om_response(200, {"replicaSets": [], "sharding": [], "processes": []}),
        ]
    )
    observed_timeouts = []
    hosts_reads = []

    def get_object(*args, _request_timeout):
        observed_timeouts.append(_request_timeout[0])
        clock.now += 0.25
        raise ApiException(status=next(kubernetes_statuses))

    monkeypatch.setattr(
        KubernetesTester, "clients", lambda name: SimpleNamespace(get_namespaced_custom_object=get_object)
    )

    def request(method, url, *, timeout, **kwargs):
        observed_timeouts.append(timeout)
        clock.now += 0.25
        if url.endswith("/automationConfig"):
            return next(configs)
        if url.endswith("/hosts"):
            hosts_reads.append(url)
            return om_response(200, {"results": []})
        pytest.fail(f"unexpected Ops Manager URL: {url}")

    monkeypatch.setattr(requests, "request", request)

    KubernetesTester.wait_condition({"wait_until": "mongo_resource_deleted", "timeout": 20, "use_shared_poller": True})

    assert len(observed_timeouts) == 9
    assert all(0 < later < earlier for earlier, later in zip(observed_timeouts, observed_timeouts[1:]))
    assert len(hosts_reads) == 2
    assert clock.now < 20


def test_shared_deletion_wait_fails_fast_on_kubernetes_forbidden(clock, monkeypatch):
    monkeypatch.setattr(KubernetesTester, "namespace", "test", raising=False)
    monkeypatch.setattr(KubernetesTester, "name", "database", raising=False)
    monkeypatch.setattr(KubernetesTester, "kind", "MongoDB", raising=False)
    api = MagicMock()
    api.get_namespaced_custom_object.side_effect = ApiException(status=403)
    monkeypatch.setattr(KubernetesTester, "clients", lambda name: api)

    with pytest.raises(ApiException) as error:
        KubernetesTester.wait_condition(
            {"wait_until": "mongo_resource_deleted", "timeout": 20, "use_shared_poller": True}
        )

    assert error.value.status == 403
    assert api.get_namespaced_custom_object.call_count == 1
    assert clock.now == 0


def test_shared_deletion_wait_rejects_late_success(clock, monkeypatch):
    monkeypatch.setattr(KubernetesTester, "namespace", "test", raising=False)
    monkeypatch.setattr(KubernetesTester, "name", "database", raising=False)
    monkeypatch.setattr(KubernetesTester, "kind", "MongoDB", raising=False)
    ops_manager_read = MagicMock()
    monkeypatch.setattr(KubernetesTester, "is_om_state_cleaned", ops_manager_read)

    def get_object(*args, **kwargs):
        clock.now += 3
        raise ApiException(status=404)

    monkeypatch.setattr(
        KubernetesTester, "clients", lambda name: SimpleNamespace(get_namespaced_custom_object=get_object)
    )

    with pytest.raises(PollTimeoutError):
        KubernetesTester.wait_condition(
            {"wait_until": "mongo_resource_deleted", "timeout": 2, "use_shared_poller": True}
        )

    ops_manager_read.assert_not_called()


def test_shared_deletion_wait_stops_during_unfinished_group_discovery(clock, monkeypatch, ops_manager_environment):
    monkeypatch.setattr(KubernetesTester, "group_id", None)
    monkeypatch.setattr(KubernetesTester, "namespace", "test", raising=False)
    monkeypatch.setattr(KubernetesTester, "name", "database", raising=False)
    monkeypatch.setattr(KubernetesTester, "kind", "MongoDB", raising=False)

    def get_object(*args, **kwargs):
        raise ApiException(status=404)

    monkeypatch.setattr(
        KubernetesTester, "clients", lambda name: SimpleNamespace(get_namespaced_custom_object=get_object)
    )
    timeouts = []

    def request(method, url, *, timeout, **kwargs):
        timeouts.append(timeout)
        clock.now += 1
        return om_response(200, {"results": [], "links": [{"rel": "next"}]})

    monkeypatch.setattr(requests, "request", request)

    with pytest.raises(PollTimeoutError, match="mongo_resource_deleted"):
        KubernetesTester.wait_condition(
            {"wait_until": "mongo_resource_deleted", "timeout": 2, "use_shared_poller": True}
        )

    assert timeouts == [2, 1]
    assert clock.now == 2


def test_legacy_wait_condition_still_splits_time_between_phases(monkeypatch):
    wait_until = MagicMock()
    monkeypatch.setattr(KubernetesTester, "wait_until", wait_until)

    KubernetesTester.wait_condition({"wait_until": "first, second", "timeout": 20})

    assert wait_until.call_args_list == [mock_call("first", 10), mock_call("second", 10)]


def test_replica_set_deletion_uses_shared_poller():
    test_file = Path(__file__).parents[2] / "tests" / "replicaset" / "replica_set.py"
    module = ast.parse(test_file.read_text())
    delete_class = next(
        node for node in module.body if isinstance(node, ast.ClassDef) and node.name == "TestReplicaSetDelete"
    )
    action = KubernetesTester.doc_string_to_init(ast.get_docstring(delete_class))["delete"]

    assert action["wait_until"] == "mongo_resource_deleted"
    assert action["timeout"] == 600
    assert action["use_shared_poller"] is True


def test_get_hosts_validates_ops_manager_host_before_group_lookup(monkeypatch):
    monkeypatch.delenv("OM_HOST", raising=False)
    monkeypatch.delenv("NAMESPACE", raising=False)
    monkeypatch.setattr(KubernetesTester, "group_id", None)
    group_lookup = MagicMock()
    monkeypatch.setattr(KubernetesTester, "get_om_group_id", group_lookup)

    with pytest.raises(ValueError, match="OM_HOST"):
        KubernetesTester.get_hosts()

    group_lookup.assert_not_called()

"""Tests for the Cloud QA janitor (scripts/evergreen/e2e/setup_cloud_qa.py).

The periodic teardown runs against a real Cloud QA org; these tests pin the load-shaping behaviour: a project that
is already gone is not retried, only transient failures are retried (resending only the PUTs that failed, with
exponential backoff), repeated failures and an exhausted time budget stop the run, and listing errors fail loudly.
Every test goes through requests.Session.request, the one seam of all requests calls.
"""

import json
from types import SimpleNamespace
from unittest.mock import Mock, call

import pytest
import requests

from scripts.evergreen.e2e import setup_cloud_qa as qa

FAKE_BASE_URL = "https://cloud-qa.example.com"
FAKE_ORG_ID = "000000000000000000000001"
FAKE_GROUP_ID = "000000000000000000000002"
GROUP_URL = f"{FAKE_BASE_URL}/api/public/v1.0/groups/{FAKE_GROUP_ID}"
CONTROLLED_FEATURE = ("PUT", f"{GROUP_URL}/controlledFeature")
AUTOMATION_CONFIG = ("PUT", f"{GROUP_URL}/automationConfig")
PUTS = [CONTROLLED_FEATURE, AUTOMATION_CONFIG]
DELETE = ("DELETE", GROUP_URL)
CLOCK_ORIGIN = 1_000_000.0  # not 0, so that a deadline computed from 0 fails the tests
MINUTE = 60
REQUEST_TIMEOUT = (5, 60)


def response(status_code: int, body: str = "", url: str = FAKE_BASE_URL) -> requests.Response:
    result = requests.Response()
    result.status_code = status_code
    result._content = body.encode()
    result.request = requests.Request("GET", url).prepare()
    return result


class FakeRequests:
    """Replaces requests.Session.request: serves the scripted status codes, (status code, body) tuples or exceptions."""

    def __init__(self, *scripted):
        self.scripted = list(scripted)
        self.calls = []
        self.kwargs = []

    def __call__(self, method, url, **kwargs):
        self.calls.append((method.upper(), url))
        if kwargs.get("params"):
            kwargs["params"] = dict(kwargs["params"])  # the code changes the page number in place
        self.kwargs.append(kwargs)
        scripted = self.scripted.pop(0)
        if isinstance(scripted, Exception):
            raise scripted
        status_code, body = scripted if isinstance(scripted, tuple) else (scripted, "")
        return response(status_code, body, url=url)


class FakeTime:
    """A clock that only moves when told to, or when something sleeps."""

    def __init__(self):
        self.now = CLOCK_ORIGIN
        self.sleeps = []

    def time(self):
        return self.now

    def monotonic(self):
        return self.now

    def sleep(self, seconds):
        self.sleeps.append(seconds)
        self.now += seconds


@pytest.fixture(autouse=True)
def env(monkeypatch):
    monkeypatch.setenv(qa.BASE_URL, FAKE_BASE_URL)
    monkeypatch.setenv(qa.APIKEY_OWNER, "secret")
    monkeypatch.setenv(qa.USER_OWNER, "user")
    monkeypatch.setenv(qa.ORG_ID, FAKE_ORG_ID)
    monkeypatch.setattr(qa, "read_env_file", lambda: {"OM_USER": "project-user", "OM_API_KEY": "project-key"})
    monkeypatch.setattr(requests.Session, "request", Mock(side_effect=AssertionError("unexpected request")))


@pytest.fixture
def clock(monkeypatch):
    fake = FakeTime()
    monkeypatch.setattr(qa, "time", fake)
    # random() is 0.5: the backoffs are 3.5, 6.5 and 12.5 seconds; the projects stay in the order they are listed
    monkeypatch.setattr(qa, "random", SimpleNamespace(random=lambda: 0.5, shuffle=lambda items: None))
    return fake


def fake_requests(monkeypatch, *scripted) -> FakeRequests:
    fake = FakeRequests(*scripted)
    monkeypatch.setattr(requests.Session, "request", fake)
    return fake


@pytest.mark.parametrize(
    "scripted,expected_status,expected_calls,expected_sleeps",
    [
        pytest.param([200, 200, 202], 202, [*PUTS, DELETE], [], id="removed"),
        pytest.param([404, 404], 404, [CONTROLLED_FEATURE, DELETE], [], id="first-put-404-skips-the-other-put"),
        pytest.param([200, 404, 404], 404, [*PUTS, DELETE], [], id="second-put-404"),
        pytest.param([404, 202], 202, [CONTROLLED_FEATURE, DELETE], [], id="put-404-delete-decides"),
        pytest.param([200, 200, 404], 404, [*PUTS, DELETE], [], id="delete-404"),
        pytest.param([403, 200, 202], 202, [*PUTS, DELETE], [], id="put-403-does-not-skip-the-rest"),
        pytest.param(
            [200, 200, 500, 500, 202],
            202,
            [*PUTS, DELETE, DELETE, DELETE],
            [3.5, 6.5],
            id="transient-delete-is-retried-without-the-puts",
        ),
        pytest.param([200, 200, 429, 202], 202, [*PUTS, DELETE, DELETE], [3.5], id="429-is-retried"),
        pytest.param([200, 200, 500, 404], 404, [*PUTS, DELETE, DELETE], [3.5], id="delete-500-then-404"),
        pytest.param([200, 200, 500, 502, 503], 503, [*PUTS, DELETE, DELETE, DELETE], [3.5, 6.5], id="gives-up"),
        pytest.param(
            [200, 500, 500, 200, 202],
            202,
            [*PUTS, DELETE, AUTOMATION_CONFIG, DELETE],
            [3.5],
            id="failed-put-is-sent-again",
        ),
        pytest.param(
            [404, 500, 404, 404],
            404,
            [CONTROLLED_FEATURE, DELETE, CONTROLLED_FEATURE, DELETE],
            [3.5],
            id="put-404-does-not-stop-the-retry",
        ),
        pytest.param(
            [403, 200, 500, 200, 202],
            202,
            [*PUTS, DELETE, CONTROLLED_FEATURE, DELETE],
            [3.5],
            id="forbidden-put-is-sent-again",
        ),
        pytest.param(
            [429, 200, 200, 202],
            202,
            [CONTROLLED_FEATURE, CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE],
            [3.5],
            id="first-put-429-ends-the-attempt",
        ),
        pytest.param(
            [200, 429, 200, 202],
            202,
            [CONTROLLED_FEATURE, AUTOMATION_CONFIG, AUTOMATION_CONFIG, DELETE],
            [3.5],
            id="second-put-429-ends-the-attempt",
        ),
        pytest.param(
            [requests.ConnectionError("reset"), 200, 200, 202],
            202,
            [CONTROLLED_FEATURE, CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE],
            [3.5],
            id="put-without-response-ends-the-attempt",
        ),
        pytest.param(
            [429] * 3,
            429,
            [CONTROLLED_FEATURE] * 3,
            [3.5, 6.5],
            id="put-keeps-failing-with-429",
        ),
        pytest.param([200, 200, 409], 409, [*PUTS, DELETE], [], id="409-is-final"),
        pytest.param([200, 200, 401], 401, [*PUTS, DELETE], [], id="401-is-final"),
        pytest.param([200, 200, 403], 403, [*PUTS, DELETE], [], id="403-is-final"),
        pytest.param([200, 500, 409], 409, [*PUTS, DELETE], [], id="failed-put-and-409-is-final"),
        pytest.param(
            [200, 200, requests.ConnectionError("reset"), requests.Timeout("slow"), 202],
            202,
            [*PUTS, DELETE, DELETE, DELETE],
            [3.5, 6.5],
            id="delete-without-response-is-retried",
        ),
        pytest.param(
            [200, 200, *[requests.ConnectionError("reset")] * 3],
            qa.CONNECTION_ERROR_STATUS,
            [*PUTS, DELETE, DELETE, DELETE],
            [3.5, 6.5],
            id="delete-never-responds",
        ),
    ],
)
def test_remove_group_by_id(monkeypatch, clock, scripted, expected_status, expected_calls, expected_sleeps):
    fake = fake_requests(monkeypatch, *scripted)

    assert qa.remove_group_by_id(FAKE_GROUP_ID).status_code == expected_status
    assert fake.calls == expected_calls
    assert clock.sleeps == expected_sleeps


def test_remove_group_by_id_sends_the_payloads_with_auth_and_timeout(monkeypatch, clock):
    fake = fake_requests(monkeypatch, 200, 200, 202)

    qa.remove_group_by_id(FAKE_GROUP_ID)

    assert [kwargs.get("json") for kwargs in fake.kwargs] == [
        {"externalManagementSystem": {"name": "mongodb-enterprise-operator"}, "policies": []},
        {},
        None,
    ]
    for kwargs in fake.kwargs:
        assert (kwargs["auth"].username, kwargs["auth"].password) == ("user", "secret")
        assert kwargs["timeout"] == REQUEST_TIMEOUT


def test_remove_group_by_id_backs_off_exponentially(monkeypatch, clock):
    monkeypatch.setattr(qa, "MAX_REMOVE_ATTEMPTS", 4)
    fake_requests(monkeypatch, 200, 200, 500, 500, 500, 500)

    qa.remove_group_by_id(FAKE_GROUP_ID)

    assert clock.sleeps == [3.5, 6.5, 12.5]


def test_remove_group_by_id_does_not_retry_a_programming_error(monkeypatch, clock):
    fake = fake_requests(monkeypatch, ValueError("bug"))

    with pytest.raises(ValueError):
        qa.remove_group_by_id(FAKE_GROUP_ID)

    assert len(fake.calls) == 1


@pytest.mark.parametrize(
    "scripted,expected_logged,expected_not_logged",
    [
        pytest.param([200, 200, (409, "CANNOT_CLOSE")], [f"DELETE {GROUP_URL} -> 409 CANNOT_CLOSE\n"], [], id="delete"),
        pytest.param(
            [200, (500, "PUT_FAILED"), (409, "CANNOT_CLOSE")],
            [f"PUT {GROUP_URL}/automationConfig -> 500 PUT_FAILED\n"],
            [],
            id="put",
        ),
        pytest.param(
            [200, 200, (409, "x" * 2000)],
            [f"-> 409 {'x' * 500}\n"],
            ["x" * 501],
            id="the-body-is-cut",
        ),
        pytest.param(
            [(200, "OK_BODY"), (200, "OK_BODY"), (202, "OK_BODY")],
            [f"DELETE {GROUP_URL} -> 202\n"],
            ["OK_BODY"],
            id="no-body-on-success",
        ),
        pytest.param(
            [(404, "GONE_BODY"), (404, "GONE_BODY")],
            [f"PUT {GROUP_URL}/controlledFeature -> 404\n", f"DELETE {GROUP_URL} -> 404\n"],
            ["GONE_BODY"],
            id="no-body-on-404",
        ),
        pytest.param(
            [200, 200, requests.ConnectionError("reset by peer")],
            [f"DELETE {GROUP_URL} -> ConnectionError('reset by peer')\n"],
            ["599"],
            id="no-response",
        ),
    ],
)
def test_remove_group_by_id_logs_the_status_and_the_failure_body(
    monkeypatch, clock, capsys, scripted, expected_logged, expected_not_logged
):
    monkeypatch.setattr(qa, "MAX_REMOVE_ATTEMPTS", 1)
    fake_requests(monkeypatch, *scripted)

    qa.remove_group_by_id(FAKE_GROUP_ID)

    output = capsys.readouterr().out
    for line in expected_logged:
        assert line in output
    for text in expected_not_logged:
        assert text not in output


@pytest.mark.parametrize("status_code,expected_log", [(404, "OK"), (202, "OK"), (409, "FAILED"), (500, "FAILED")])
def test_remove_group_by_name(monkeypatch, capsys, status_code, expected_log):
    removed = []
    monkeypatch.setattr(qa, "get_group_id_by_name", lambda name: FAKE_GROUP_ID if name == "a-1-x" else "wrong")
    monkeypatch.setattr(qa, "remove_group_by_id", lambda group_id: removed.append(group_id) or response(status_code))

    result = qa.remove_group_by_name("a-1-x")

    assert removed == [FAKE_GROUP_ID]
    assert result.status_code == status_code
    assert f"Removing group id: {FAKE_GROUP_ID} and name: a-1-x -> {expected_log}\n" in capsys.readouterr().out


def fake_projects(count: int) -> list:
    return [{"id": f"id-{i}", "name": f"a-1-{i}"} for i in range(count)]


def run_clean_unused_projects(monkeypatch, clock, statuses: list, listing_seconds=0, removal_seconds=0) -> list:
    listed = fake_projects(len(statuses))
    outcomes = iter(statuses)
    removed = []

    def fake_listing(*args, **kwargs):
        clock.now += listing_seconds
        return listed, []

    def fake_remove(group_id):
        removed.append(group_id)
        clock.now += removal_seconds
        return response(next(outcomes))

    monkeypatch.setattr(qa, "get_projects_older_than", fake_listing)
    monkeypatch.setattr(qa, "remove_group_by_id", fake_remove)

    qa.clean_unused_projects(FAKE_ORG_ID)

    return removed


@pytest.mark.parametrize(
    "statuses,expected_attempted,expected_stop,expected_output",
    [
        pytest.param([202, 404, 409], 3, False, ["Removed 1", "Already gone 1", "Kept 1 projects:"], id="mixed"),
        pytest.param([404, 404, 202, 202, 202], 5, False, ["Removed 3", "Already gone 2", "Kept 0 projects:"], id="ok"),
        # failures that point at Cloud QA or at the credentials stop the run
        pytest.param([500] * 7, 5, True, ["Kept 5 projects:"], id="500"),
        pytest.param([500] * 5, 5, True, ["Kept 5 projects:"], id="the-last-five-fail"),
        pytest.param([502, 503, 504, 500, 599, 500], 5, True, [], id="other-server-errors"),
        pytest.param([429] * 7, 5, True, [], id="429"),
        pytest.param([401] * 7, 5, True, [], id="401"),
        pytest.param([403] * 7, 5, True, [], id="403"),
        pytest.param([500] * 4 + [202] + [500] * 4, 9, False, ["Removed 1", "Kept 8 projects:"], id="202-resets"),
        pytest.param([500] * 4 + [404] + [500] * 4, 9, False, ["Already gone 1", "Kept 8 projects:"], id="404-resets"),
        # a conflict is a problem of the project itself: it neither counts nor resets
        pytest.param([500] * 4 + [409] + [500] * 3, 6, True, ["Kept 6 projects:"], id="409-does-not-reset"),
        pytest.param([409] * 8, 8, False, ["Kept 8 projects:"], id="409-does-not-count"),
    ],
)
def test_clean_unused_projects(
    monkeypatch, clock, capsys, statuses, expected_attempted, expected_stop, expected_output
):
    removed = run_clean_unused_projects(monkeypatch, clock, statuses)

    output = capsys.readouterr().out
    assert removed == [f"id-{i}" for i in range(expected_attempted)]
    assert f"Not attempted {len(statuses) - expected_attempted} projects." in output
    assert ("Stopping: 5 consecutive failures" in output) == expected_stop
    for line in expected_output:
        assert line in output


@pytest.mark.parametrize(
    "listing_seconds,removal_seconds,expected_attempted",
    [
        # the budget is 90 minutes: the projects start at 0, 40 and 80 minutes
        pytest.param(0, 40 * MINUTE, 3, id="removals-use-up-the-budget"),
        # the time spent listing counts against the budget
        pytest.param(80 * MINUTE, 10 * MINUTE, 2, id="listing-uses-up-the-budget"),
        pytest.param(90 * MINUTE + 1, 0, 0, id="listing-exceeds-the-budget"),
        pytest.param(90 * MINUTE, 0, 10, id="budget-is-not-yet-exceeded"),
        pytest.param(0, 0, 10, id="within-the-budget"),
    ],
)
def test_clean_unused_projects_time_budget(
    monkeypatch, clock, capsys, listing_seconds, removal_seconds, expected_attempted
):
    removed = run_clean_unused_projects(monkeypatch, clock, [202] * 10, listing_seconds, removal_seconds)

    output = capsys.readouterr().out
    assert removed == [f"id-{i}" for i in range(expected_attempted)]
    assert f"Not attempted {10 - expected_attempted} projects." in output
    assert output.count("Stopping: the time budget is exhausted") == (1 if expected_attempted < 10 else 0)


def test_clean_unused_projects_shuffles_the_projects(monkeypatch, clock):
    monkeypatch.setattr(qa.random, "shuffle", lambda items: items.reverse())

    removed = run_clean_unused_projects(monkeypatch, clock, [202] * 3)

    assert removed == ["id-2", "id-1", "id-0"]


def test_clean_unused_projects_lists_the_old_projects_and_summarizes(monkeypatch, clock, capsys):
    listing_calls = []

    def fake_listing(*args, **kwargs):
        listing_calls.append((args, kwargs))
        return fake_projects(1), [{"id": "newer-1"}, {"id": "newer-2"}]

    monkeypatch.setattr(qa, "get_projects_older_than", fake_listing)
    monkeypatch.setattr(qa, "remove_group_by_id", lambda group_id: response(409))

    qa.clean_unused_projects(FAKE_ORG_ID)

    output = capsys.readouterr().out
    assert listing_calls == [((FAKE_ORG_ID,), {"minutes_interval": 140})]
    assert "Ignored 2 projects because they are too new." in output
    assert "\ta-1-0\tid-0\n" in output


def test_clean_unused_keys_keeps_the_keys_in_use_and_continues_after_a_failure(monkeypatch, capsys):
    keys = [
        {"id": "own", "publicKey": "user", "desc": "1"},
        {"id": "evg", "publicKey": "a", "desc": "EVG_DO_NOT_DELETE"},
        {"id": "not-delete", "publicKey": "b", "desc": "NOT_DELETE_1"},
        {"id": "old-1", "publicKey": "c", "desc": "1"},
        {"id": "failing", "publicKey": "d", "desc": "2"},
        {"id": "old-2", "publicKey": "e", "desc": "3"},
    ]
    listing_calls = []
    deleted = []

    def fake_delete(org_id, key_id):
        if key_id == "failing":
            raise Exception("boom")
        deleted.append((org_id, key_id))

    monkeypatch.setattr(
        qa, "get_keys_older_than", lambda *args, **kwargs: listing_calls.append((args, kwargs)) or (keys, [{}])
    )
    monkeypatch.setattr(qa, "delete_api_key", fake_delete)

    qa.clean_unused_keys(FAKE_ORG_ID)

    output = capsys.readouterr().out
    assert listing_calls == [((FAKE_ORG_ID,), {"minutes_interval": 140})]
    assert deleted == [(FAKE_ORG_ID, "old-1"), (FAKE_ORG_ID, "old-2")]
    assert "Got an exception trying to remove the key boom" in output
    assert "Ignored 1 keys because they are too new." in output
    assert "Removed 2 keys." in output
    assert "Kept 4:" in output


def test_unconfigure_all_cleans_keys_even_when_the_project_cleanup_fails(monkeypatch):
    cleanups = Mock()
    cleanups.projects.side_effect = Exception("listing failed")
    monkeypatch.setattr(qa, "read_env_file", Mock(side_effect=FileNotFoundError))
    monkeypatch.setattr(qa, "read_namespace_from_file", Mock(side_effect=FileNotFoundError))
    monkeypatch.setattr(qa, "clean_unused_projects", cleanups.projects)
    monkeypatch.setattr(qa, "clean_unused_keys", cleanups.keys)

    with pytest.raises(Exception, match="listing failed"):
        qa.unconfigure_all()

    assert cleanups.mock_calls == [call.projects(FAKE_ORG_ID), call.keys(FAKE_ORG_ID)]


def test_unconfigure_all_cleans_projects_and_keys_when_removing_its_own_key_fails(monkeypatch):
    cleanups = Mock()
    monkeypatch.setattr(qa, "read_env_file", lambda: {"OM_KEY_ID": "own-key"})
    monkeypatch.setattr(qa, "read_namespace_from_file", Mock(side_effect=FileNotFoundError))
    monkeypatch.setattr(qa, "delete_api_key", Mock(side_effect=Exception("boom")))
    monkeypatch.setattr(qa, "clean_unused_projects", cleanups.projects)
    monkeypatch.setattr(qa, "clean_unused_keys", cleanups.keys)

    qa.unconfigure_all()

    assert cleanups.mock_calls == [call.projects(FAKE_ORG_ID), call.keys(FAKE_ORG_ID)]


@pytest.mark.parametrize(
    "status_code,expected_raises",
    [
        pytest.param(204, False, id="removed"),
        pytest.param(404, False, id="already-gone"),
        pytest.param(401, True, id="401"),
        pytest.param(500, True, id="500"),
    ],
)
def test_delete_api_key(monkeypatch, status_code, expected_raises):
    fake = fake_requests(monkeypatch, status_code)

    if expected_raises:
        with pytest.raises(Exception, match="Could not remove the Programmatic API Key"):
            qa.delete_api_key(FAKE_ORG_ID, "key-id")
    else:
        assert qa.delete_api_key(FAKE_ORG_ID, "key-id").status_code == status_code
    assert fake.calls == [("DELETE", f"{FAKE_BASE_URL}/api/public/v1.0/orgs/{FAKE_ORG_ID}/apiKeys/key-id")]
    assert (fake.kwargs[0]["auth"].username, fake.kwargs[0]["auth"].password) == ("user", "secret")


@pytest.mark.parametrize(
    "call,scripted",
    [
        pytest.param(lambda: qa.create_api_key(FAKE_ORG_ID, "description"), [(200, "{}")], id="create_api_key"),
        pytest.param(lambda: qa.whitelist_key(FAKE_ORG_ID, "key-id"), [200], id="whitelist_key"),
        pytest.param(lambda: qa.get_group_id_by_name("a-1-x"), [(200, '{"id": "group"}')], id="get_group_id_by_name"),
        pytest.param(lambda: qa.delete_api_key(FAKE_ORG_ID, "key-id"), [204], id="delete_api_key"),
        pytest.param(lambda: qa.get_projects_older_than(FAKE_ORG_ID), [(200, '{"results": []}')], id="projects"),
        pytest.param(lambda: qa.get_keys_older_than(FAKE_ORG_ID), [(200, '{"results": []}')], id="keys"),
        pytest.param(lambda: qa.remove_group_by_id(FAKE_GROUP_ID), [200, 200, 202], id="remove_group_by_id"),
    ],
)
def test_every_request_has_a_timeout(monkeypatch, clock, call, scripted):
    fake = fake_requests(monkeypatch, *scripted)

    call()

    assert fake.kwargs
    assert all(kwargs["timeout"] == REQUEST_TIMEOUT for kwargs in fake.kwargs)


LISTINGS = [
    pytest.param(qa.get_projects_older_than, "groups", id="projects"),
    pytest.param(qa.get_keys_older_than, "apiKeys", id="keys"),
]


@pytest.mark.parametrize("listing,expected_path", LISTINGS)
@pytest.mark.parametrize(
    "status_code,body",
    [
        pytest.param(401, "unauthorized", id="401"),
        pytest.param(404, "not found", id="404"),
        pytest.param(500, "boom", id="500"),
        pytest.param(502, "<html>Bad Gateway</html>", id="non-json-body"),
        pytest.param(503, "x" * 2000, id="the-body-is-cut"),
    ],
)
def test_listing_failure_raises_instead_of_returning_no_results(monkeypatch, listing, expected_path, status_code, body):
    fake_requests(monkeypatch, (status_code, body))

    with pytest.raises(Exception) as raised:
        listing(FAKE_ORG_ID)

    expected_url = f"{FAKE_BASE_URL}/api/public/v1.0/orgs/{FAKE_ORG_ID}/{expected_path}"
    assert str(raised.value) == f"Could not list {expected_url}: {status_code} {body[:500]}"


@pytest.mark.parametrize("listing,expected_path", LISTINGS)
def test_listing_failure_on_a_later_page_raises(monkeypatch, listing, expected_path):
    fake_requests(monkeypatch, (200, '{"results": [{"name": "a-1-x", "id": "1", "desc": "1"}]}'), (500, "boom"))

    with pytest.raises(Exception, match="500 boom"):
        listing(FAKE_ORG_ID)


@pytest.mark.parametrize("listing,expected_path", LISTINGS)
def test_listing_combines_the_pages_and_splits_them_by_age(monkeypatch, clock, listing, expected_path):
    def item(item_id: str, minutes_old: int) -> dict:
        created = int(clock.time()) - minutes_old * MINUTE
        return {"id": item_id, "name": f"a-{created}-x", "desc": str(created)}

    fake = fake_requests(
        monkeypatch,
        (200, json.dumps({"results": [item("old-1", 141), item("new-1", 139)]})),
        (200, json.dumps({"results": [item("old-2", 500)]})),
        (200, '{"results": []}'),
    )

    old, new = listing(FAKE_ORG_ID, minutes_interval=140)

    assert [i["id"] for i in old] == ["old-1", "old-2"]
    assert [i["id"] for i in new] == ["new-1"]
    assert {url for _, url in fake.calls} == {f"{FAKE_BASE_URL}/api/public/v1.0/orgs/{FAKE_ORG_ID}/{expected_path}"}
    assert [kwargs["params"]["pageNum"] for kwargs in fake.kwargs] == [1, 2, 3]
    assert all(kwargs["params"]["itemsPerPage"] == 500 for kwargs in fake.kwargs)
    assert all((kwargs["auth"].username, kwargs["auth"].password) == ("user", "secret") for kwargs in fake.kwargs)


@pytest.mark.parametrize("listing,expected_path", LISTINGS)
def test_listing_stops_after_the_page_limit(monkeypatch, listing, expected_path):
    page = (200, '{"results": [{"name": "a-1-x", "id": "1", "desc": "1"}]}')
    fake = fake_requests(monkeypatch, *[page] * 101)

    listing(FAKE_ORG_ID)

    assert len(fake.calls) == 101

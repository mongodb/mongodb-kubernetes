"""Tests for the Cloud QA janitor (scripts/evergreen/e2e/setup_cloud_qa.py).

The periodic teardown runs against a real Cloud QA org; these tests pin the load-shaping behaviour: a project that
is already gone is not retried, only transient failures are retried (resending only the PUTs that failed, with
exponential backoff), repeated failures and an exhausted time budget stop the run, and listing errors fail loudly.
"""

from types import SimpleNamespace

import pytest
import requests

from scripts.evergreen.e2e import setup_cloud_qa as qa

FAKE_BASE_URL = "https://cloud-qa.example.com"
FAKE_ORG_ID = "000000000000000000000001"
FAKE_GROUP_ID = "000000000000000000000002"
GROUP_URL = f"{FAKE_BASE_URL}/api/public/v1.0/groups/{FAKE_GROUP_ID}"
CONTROLLED_FEATURE = ("PUT", f"{GROUP_URL}/controlledFeature")
AUTOMATION_CONFIG = ("PUT", f"{GROUP_URL}/automationConfig")
DELETE = ("DELETE", GROUP_URL)
CLOCK_ORIGIN = 1000.0
MINUTE = 60


def response(status_code: int, body=None, url: str = FAKE_BASE_URL) -> requests.Response:
    result = requests.Response()
    result.status_code = status_code
    result._content = (body if body is not None else '{"detail": "boom"}').encode()
    result.request = requests.Request("GET", url).prepare()
    return result


class FakeRequest:
    """Stands in for requests.request: serves the scripted status codes, (status code, body) tuples or exceptions."""

    def __init__(self, *scripted):
        self.scripted = list(scripted)
        self.calls = []
        self.kwargs = []

    def __call__(self, method, url, **kwargs):
        self.calls.append((method, url))
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


@pytest.fixture
def clock(monkeypatch):
    fake = FakeTime()
    monkeypatch.setattr(qa, "time", fake)
    monkeypatch.setattr(qa, "random", SimpleNamespace(random=lambda: 0.5))
    return fake


def fake_requests(monkeypatch, *scripted) -> FakeRequest:
    fake = FakeRequest(*scripted)
    monkeypatch.setattr(qa.requests, "request", fake)
    return fake


@pytest.mark.parametrize(
    "scripted,expected_status,expected_calls,expected_sleeps",
    [
        ([200, 200, 202], 202, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE], []),
        # a PUT 404 skips the remaining PUTs, only the DELETE decides whether the group is gone
        ([404, 404], 404, [CONTROLLED_FEATURE, DELETE], []),
        ([200, 404, 404], 404, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE], []),
        ([404, 202], 202, [CONTROLLED_FEATURE, DELETE], []),
        ([200, 200, 404], 404, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE], []),
        # transient DELETE failures are retried and the PUTs are not repeated
        (
            [200, 200, 500, 500, 202],
            202,
            [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE, DELETE, DELETE],
            [3.5, 6.5],
        ),
        ([200, 200, 429, 202], 202, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE, DELETE], [3.5]),
        ([200, 200, 500, 404], 404, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE, DELETE], [3.5]),
        # no sleep after the last attempt
        (
            [200, 200, 500, 502, 503],
            503,
            [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE, DELETE, DELETE],
            [3.5, 6.5],
        ),
        # a PUT that failed is sent again with the retry, the one that succeeded is not
        (
            [200, 500, 500, 200, 202],
            202,
            [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE, AUTOMATION_CONFIG, DELETE],
            [3.5],
        ),
        # a PUT 404 does not stop the retry of a transient DELETE failure
        (
            [404, 500, 404, 404],
            404,
            [CONTROLLED_FEATURE, DELETE, CONTROLLED_FEATURE, DELETE],
            [3.5],
        ),
        # failures that cannot change on retry are not retried
        ([200, 200, 409], 409, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE], []),
        ([200, 200, 401], 401, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE], []),
        ([200, 200, 403], 403, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE], []),
        ([200, 500, 409], 409, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE], []),
        # a request that gets no response is a transient failure
        (
            [200, 200, requests.ConnectionError("reset"), requests.Timeout("slow"), 202],
            202,
            [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE, DELETE, DELETE],
            [3.5, 6.5],
        ),
        (
            [200, 200] + [requests.ConnectionError("reset")] * 3,
            qa.CONNECTION_ERROR_STATUS,
            [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE, DELETE, DELETE],
            [3.5, 6.5],
        ),
        ([requests.ConnectionError("reset"), 200, 202], 202, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE], []),
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
        assert kwargs["timeout"] == qa.REQUEST_TIMEOUT


def test_remove_group_by_id_backs_off_exponentially(monkeypatch, clock):
    monkeypatch.setattr(qa, "MAX_REMOVE_ATTEMPTS", 4)
    fake_requests(monkeypatch, 200, 200, 500, 500, 500, 500)

    qa.remove_group_by_id(FAKE_GROUP_ID)

    assert clock.sleeps == [3.5, 6.5, 12.5]


@pytest.mark.parametrize(
    "scripted,expected_logged,expected_not_logged",
    [
        ([200, 200, (409, "CANNOT_CLOSE")], [f"DELETE {GROUP_URL} -> 409 CANNOT_CLOSE\n"], []),
        (
            [200, (500, "PUT_FAILED"), (409, "CANNOT_CLOSE")],
            [f"PUT {GROUP_URL}/automationConfig -> 500 PUT_FAILED\n"],
            [],
        ),
        ([200, 200, (409, "x" * 2000)], [f"-> 409 {'x' * qa.MAX_LOGGED_BODY}\n"], ["x" * (qa.MAX_LOGGED_BODY + 1)]),
        ([(200, "OK_BODY"), (200, "OK_BODY"), (202, "OK_BODY")], [f"DELETE {GROUP_URL} -> 202\n"], ["OK_BODY"]),
        ([(404, "GONE_BODY"), (404, "GONE_BODY")], [f"DELETE {GROUP_URL} -> 404\n"], ["GONE_BODY"]),
        ([200, 200, requests.ConnectionError("reset by peer")], [f"DELETE {GROUP_URL} -> ConnectionError"], []),
    ],
)
def test_remove_group_by_id_logs_every_call(monkeypatch, clock, capsys, scripted, expected_logged, expected_not_logged):
    monkeypatch.setattr(qa, "MAX_REMOVE_ATTEMPTS", 1)
    fake_requests(monkeypatch, *scripted)

    qa.remove_group_by_id(FAKE_GROUP_ID)

    output = capsys.readouterr().out
    for line in expected_logged:
        assert line in output
    for text in expected_not_logged:
        assert text not in output


@pytest.mark.parametrize(
    "status_code,expected_log",
    [(404, "OK"), (202, "OK"), (409, "FAILED"), (500, "FAILED")],
)
def test_remove_group_by_name(monkeypatch, capsys, status_code, expected_log):
    monkeypatch.setattr(qa, "get_group_id_by_name", lambda name: FAKE_GROUP_ID)
    monkeypatch.setattr(qa, "remove_group_by_id", lambda group_id: response(status_code))

    qa.remove_group_by_name("a-1-x")

    assert f"-> {expected_log}\n" in capsys.readouterr().out


def projects(count: int) -> list:
    return [{"id": f"id-{i}", "name": f"a-1-{i}"} for i in range(count)]


def run_clean_unused_projects(
    monkeypatch, clock, statuses: list, budget_minutes=None, listing_seconds=0, removal_seconds=0
):
    listed = projects(len(statuses))
    removed = []
    listing_calls = []
    queue = list(statuses)

    def fake_listing(*args, **kwargs):
        listing_calls.append((args, kwargs))
        clock.now += listing_seconds
        return listed, []

    def fake_remove(group_id):
        removed.append(group_id)
        clock.now += removal_seconds
        return response(queue.pop(0))

    monkeypatch.setattr(qa, "get_projects_older_than", fake_listing)
    monkeypatch.setattr(qa, "remove_group_by_id", fake_remove)
    if budget_minutes is not None:
        monkeypatch.setattr(qa, "TIME_BUDGET_MINUTES", budget_minutes)

    qa.clean_unused_projects(FAKE_ORG_ID)

    assert listing_calls == [((FAKE_ORG_ID,), {"minutes_interval": 140})]
    return [project["id"] for project in listed], removed


@pytest.mark.parametrize(
    "statuses,expected_attempted,expected_output",
    [
        (
            [202, 404, 409],
            3,
            ["Removed 1 projects.", "Already gone 1 projects.", "Not attempted 0 projects.", "Kept 1 projects:"],
        ),
        ([404, 404, 202, 202, 202], 5, ["Removed 3 projects.", "Already gone 2 projects.", "Kept 0 projects:"]),
        # consecutive failures that point at Cloud QA or at the credentials stop the run
        ([500] * 7, 5, ["Stopping: 5 consecutive failures", "Kept 5 projects:", "Not attempted 2 projects."]),
        ([502, 503, 504, 500, 599, 500], 5, ["Stopping: 5 consecutive failures", "Not attempted 1 projects."]),
        ([429] * 7, 5, ["Stopping: 5 consecutive failures", "Not attempted 2 projects."]),
        ([401] * 7, 5, ["Stopping: 5 consecutive failures", "Not attempted 2 projects."]),
        ([403] * 7, 5, ["Stopping: 5 consecutive failures", "Not attempted 2 projects."]),
        ([500, 500, 500, 500, 202, 500, 500, 500, 500], 9, ["Removed 1 projects.", "Kept 8 projects:"]),
        ([500, 500, 500, 500, 404, 500, 500, 500, 500], 9, ["Already gone 1 projects.", "Kept 8 projects:"]),
        ([500, 500, 500, 500, 409, 500, 500, 500, 500], 9, ["Kept 9 projects:"]),
        # a conflict is a problem of the project itself
        ([409] * 8, 8, ["Kept 8 projects:", "Not attempted 0 projects."]),
    ],
)
def test_clean_unused_projects(monkeypatch, clock, capsys, statuses, expected_attempted, expected_output):
    listed, removed = run_clean_unused_projects(monkeypatch, clock, statuses)

    output = capsys.readouterr().out
    assert removed == listed[:expected_attempted]
    for line in expected_output:
        assert line in output
    assert ("Stopping:" in output) == (expected_attempted < len(statuses))


@pytest.mark.parametrize(
    "budget_minutes,listing_seconds,removal_seconds,expected_attempted",
    [
        (1, 0, 40, 2),
        (0.5, 0, 40, 1),
        # the default is 90 minutes: projects start at 0, 40 and 80 minutes
        (None, 0, 40 * MINUTE, 3),
        # the time spent listing counts against the budget
        (None, 80 * MINUTE, 10 * MINUTE, 2),
        (None, 0, 0, 10),
    ],
)
def test_clean_unused_projects_time_budget(
    monkeypatch, clock, capsys, budget_minutes, listing_seconds, removal_seconds, expected_attempted
):
    listed, removed = run_clean_unused_projects(
        monkeypatch, clock, [202] * 10, budget_minutes, listing_seconds, removal_seconds
    )

    output = capsys.readouterr().out
    assert removed == listed[:expected_attempted]
    assert f"Not attempted {10 - expected_attempted} projects." in output
    assert ("Stopping: the time budget is exhausted" in output) == (expected_attempted < 10)


def test_unconfigure_all_cleans_keys_even_when_the_project_cleanup_fails(monkeypatch):
    order = []

    def failing_projects(org_id):
        order.append("projects")
        raise Exception("listing failed")

    def no_file(*_):
        raise FileNotFoundError

    monkeypatch.setattr(qa, "read_env_file", no_file)
    monkeypatch.setattr(qa, "read_namespace_from_file", no_file)
    monkeypatch.setattr(qa, "clean_unused_projects", failing_projects)
    monkeypatch.setattr(qa, "clean_unused_keys", lambda org_id: order.append("keys"))

    with pytest.raises(Exception, match="listing failed"):
        qa.unconfigure_all()

    assert order == ["projects", "keys"]


@pytest.mark.parametrize("status_code,expected_raises", [(204, False), (404, False), (401, True), (500, True)])
def test_delete_api_key(monkeypatch, status_code, expected_raises):
    calls = []

    def fake_delete(url, **kwargs):
        calls.append(kwargs)
        return response(status_code)

    monkeypatch.setattr(qa.requests, "delete", fake_delete)

    if expected_raises:
        with pytest.raises(Exception, match="Could not remove the Programmatic API Key"):
            qa.delete_api_key(FAKE_ORG_ID, "key-id")
    else:
        assert qa.delete_api_key(FAKE_ORG_ID, "key-id").status_code == status_code
    assert calls[0]["timeout"] == qa.REQUEST_TIMEOUT


LISTINGS = [qa.get_projects_older_than, qa.get_keys_older_than]


@pytest.mark.parametrize("listing", LISTINGS)
@pytest.mark.parametrize("status_code", [401, 404, 500, 503])
def test_listing_failure_raises_instead_of_returning_no_results(monkeypatch, listing, status_code):
    monkeypatch.setattr(qa.requests, "get", lambda url, **_: response(status_code, url=url))

    with pytest.raises(Exception, match=f"{status_code} .*boom"):
        listing(FAKE_ORG_ID)


@pytest.mark.parametrize("listing", LISTINGS)
def test_listing_failure_with_a_non_json_body_raises_with_the_body(monkeypatch, listing):
    monkeypatch.setattr(qa.requests, "get", lambda url, **_: response(502, "<html>Bad Gateway</html>", url=url))

    with pytest.raises(Exception, match="502 <html>Bad Gateway</html>"):
        listing(FAKE_ORG_ID)


@pytest.mark.parametrize("listing", LISTINGS)
def test_listing_failure_on_a_later_page_raises(monkeypatch, listing):
    pages = [response(200, '{"results": [{"name": "a-1-x", "id": "1", "desc": "1"}]}'), response(500, "boom")]
    monkeypatch.setattr(qa.requests, "get", lambda url, **_: pages.pop(0))

    with pytest.raises(Exception, match="500 boom"):
        listing(FAKE_ORG_ID)


@pytest.mark.parametrize("listing", LISTINGS)
def test_listing_combines_the_pages(monkeypatch, listing):
    first = '{"results": [{"name": "a-1-x", "id": "1", "desc": "1"}]}'
    second = '{"results": [{"name": "a-1-y", "id": "2", "desc": "2"}]}'
    pages = [response(200, first), response(200, second), response(200, '{"results": []}')]
    requested = []

    def get(url, params, **kwargs):
        requested.append((params["pageNum"], kwargs["timeout"]))
        return pages.pop(0)

    monkeypatch.setattr(qa.requests, "get", get)

    old, new = listing(FAKE_ORG_ID)

    assert [item["id"] for item in old] == ["1", "2"]
    assert new == []
    assert requested == [(1, qa.REQUEST_TIMEOUT), (2, qa.REQUEST_TIMEOUT), (3, qa.REQUEST_TIMEOUT)]

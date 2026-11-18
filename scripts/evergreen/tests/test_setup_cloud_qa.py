"""Tests for the Cloud QA janitor (scripts/evergreen/e2e/setup_cloud_qa.py).

The periodic teardown runs against a real Cloud QA org; these tests pin the load-shaping behaviour:
a project that is already gone is not retried, only the DELETE is retried (with exponential backoff),
repeated server errors and an exhausted time budget stop the run, and listing errors fail loudly.
"""

from types import SimpleNamespace

import pytest

from scripts.evergreen.e2e import setup_cloud_qa as qa

BASE_URL = "https://cloud-qa.example.com"
ORG_ID = "6419caa39a37362fa6d3cb6d"
GROUP_ID = "656ef43cd7f0e54fe9389a13"
GROUP_URL = f"{BASE_URL}/api/public/v1.0/groups/{GROUP_ID}"
CONTROLLED_FEATURE = ("PUT", f"{GROUP_URL}/controlledFeature")
AUTOMATION_CONFIG = ("PUT", f"{GROUP_URL}/automationConfig")
DELETE = ("DELETE", GROUP_URL)


def response(status_code: int, body=None, url: str = BASE_URL) -> SimpleNamespace:
    return SimpleNamespace(
        status_code=status_code,
        text="" if body is None else str(body),
        request=SimpleNamespace(url=url),
        json=lambda: body or {},
    )


class FakeRequests:
    """Serves the scripted responses (a status code or a (status code, body) tuple) in order and records the calls."""

    def __init__(self, *scripted):
        self.scripted = list(scripted)
        self.calls = []

    def _call(self, method: str, url: str, **_):
        self.calls.append((method, url))
        status_code, *body = self.scripted.pop(0) if isinstance(self.scripted[0], tuple) else (self.scripted.pop(0),)
        return response(status_code, *body, url=url)

    def put(self, url, **kwargs):
        return self._call("PUT", url, **kwargs)

    def delete(self, url, **kwargs):
        return self._call("DELETE", url, **kwargs)


@pytest.fixture(autouse=True)
def env(monkeypatch):
    monkeypatch.setenv(qa.BASE_URL, BASE_URL)
    monkeypatch.setenv(qa.APIKEY_OWNER, "secret")
    monkeypatch.setenv(qa.USER_OWNER, "user")


@pytest.fixture
def sleeps(monkeypatch):
    recorded = []
    monkeypatch.setattr(qa.time, "sleep", recorded.append)
    return recorded


@pytest.mark.parametrize(
    "statuses,expected_status,expected_calls,expected_sleeps",
    [
        ([200, 200, 202], 202, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE], 0),
        ([404], 404, [CONTROLLED_FEATURE], 0),
        ([200, 404], 404, [CONTROLLED_FEATURE, AUTOMATION_CONFIG], 0),
        ([200, 200, 404], 404, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE], 0),
        # the PUTs are not repeated when only the DELETE fails
        ([200, 200, 500, 500, 202], 202, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE, DELETE, DELETE], 2),
        # a failing automationConfig PUT does not block the DELETE
        ([200, 500, 409, 409, 409], 409, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE, DELETE, DELETE], 2),
        # no sleep after the last attempt
        ([200, 200, 500, 500, 500], 500, [CONTROLLED_FEATURE, AUTOMATION_CONFIG, DELETE, DELETE, DELETE], 2),
    ],
)
def test_remove_group_by_id(monkeypatch, sleeps, statuses, expected_status, expected_calls, expected_sleeps):
    fake = FakeRequests(*statuses)
    monkeypatch.setattr(qa, "requests", fake)

    assert qa.remove_group_by_id(GROUP_ID).status_code == expected_status
    assert fake.calls == expected_calls
    assert len(sleeps) == expected_sleeps


def test_remove_group_by_id_backs_off_exponentially(monkeypatch, sleeps):
    monkeypatch.setattr(qa, "requests", FakeRequests(200, 200, 500, 500, 500))

    qa.remove_group_by_id(GROUP_ID)

    for attempt, delay in enumerate(sleeps):
        assert qa.BACKOFF_SECONDS * 2**attempt <= delay < qa.BACKOFF_SECONDS * 2**attempt + 1


def test_remove_group_by_id_logs_error_body(monkeypatch, sleeps, capsys):
    monkeypatch.setattr(qa, "requests", FakeRequests(200, 200, (409, "CANNOT_CLOSE_GROUP")))

    qa.remove_group_by_id(GROUP_ID, retry=1)

    output = capsys.readouterr().out
    assert f"DELETE {GROUP_URL} -> 409 CANNOT_CLOSE_GROUP" in output


def run_clean_unused_projects(monkeypatch, projects: list, statuses: list, budget_minutes=None, clock_step=0):
    removed = []
    queue = list(statuses)

    def fake_remove(group_id):
        removed.append(group_id)
        return response(queue.pop(0))

    now = [0.0]

    def fake_monotonic():
        now[0] += clock_step
        return now[0]

    monkeypatch.setattr(qa, "get_projects_older_than", lambda *_, **__: (projects, []))
    monkeypatch.setattr(qa, "remove_group_by_id", fake_remove)
    monkeypatch.setattr(qa.time, "monotonic", fake_monotonic)
    if budget_minutes is not None:
        monkeypatch.setenv(qa.TIME_BUDGET_MINUTES, str(budget_minutes))

    qa.clean_unused_projects(ORG_ID)
    return removed


def projects(count: int) -> list:
    return [{"id": f"id-{i}", "name": f"a-1-{i}"} for i in range(count)]


@pytest.mark.parametrize(
    "statuses,expected_attempted,expected_summary",
    [
        ([202, 404, 409], 3, ["Removed 1 projects.", "Already gone 1 projects.", "Kept 1 projects:"]),
        # consecutive server errors stop the run
        ([500] * 7, qa.MAX_CONSECUTIVE_SERVER_ERRORS, ["Kept 5 projects:", "Not attempted 2 projects."]),
        # a success resets the counter
        ([500] * 4 + [202] + [500] * 4, 9, ["Removed 1 projects.", "Kept 8 projects:"]),
        # 4xx other than 404 never trips the breaker
        ([409] * 8, 8, ["Kept 8 projects:"]),
    ],
)
def test_clean_unused_projects(monkeypatch, capsys, statuses, expected_attempted, expected_summary):
    removed = run_clean_unused_projects(monkeypatch, projects(len(statuses)), statuses)

    output = capsys.readouterr().out
    assert len(removed) == expected_attempted
    for line in expected_summary:
        assert line in output


def test_clean_unused_projects_stops_when_time_budget_is_exhausted(monkeypatch, capsys):
    # every clock read advances 30s; a 1 minute budget leaves room for a couple of projects only
    removed = run_clean_unused_projects(monkeypatch, projects(10), [202] * 10, budget_minutes=1, clock_step=30)

    output = capsys.readouterr().out
    assert 0 < len(removed) < 10
    assert f"Not attempted {10 - len(removed)} projects." in output
    assert "time budget" in output


@pytest.mark.parametrize(
    "function",
    [qa.get_projects_older_than, qa.get_keys_older_than],
)
def test_listing_failure_raises_instead_of_returning_no_results(monkeypatch, function):
    monkeypatch.setattr(
        qa.requests, "get", lambda url, **_: response(500, body={"errorCode": "UNEXPECTED_ERROR"}, url=url)
    )

    with pytest.raises(Exception, match="500.*UNEXPECTED_ERROR"):
        function(ORG_ID)


def test_listing_failure_on_a_later_page_raises(monkeypatch):
    pages = [response(200, {"results": [{"name": "a-1-x", "id": "1"}]}), response(500, {"detail": "boom"})]
    monkeypatch.setattr(qa.requests, "get", lambda url, **_: pages.pop(0))

    with pytest.raises(Exception, match="500"):
        qa.get_projects_older_than(ORG_ID)


def test_listing_pages_are_combined(monkeypatch):
    pages = [
        response(200, {"results": [{"name": "a-1-x", "id": "1", "desc": "1"}]}),
        response(200, {"results": []}),
    ]
    monkeypatch.setattr(qa.requests, "get", lambda url, **_: pages.pop(0))

    old, new = qa.get_projects_older_than(ORG_ID)

    assert [p["id"] for p in old] == ["1"]
    assert new == []

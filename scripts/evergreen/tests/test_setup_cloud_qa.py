"""Tests for the Cloud QA janitor (scripts/evergreen/e2e/setup_cloud_qa.py).

The periodic teardown runs against a real Cloud QA org; these tests pin the load-shaping behaviour: a project that
is already gone is not retried, only transient failures are retried (resending only the PUTs that failed, with
exponential backoff), repeated failures and an exhausted time budget stop the run, listing errors fail loudly, and
the problems of a run end in a failing exit code.
The tests that send requests go through requests.Session.request, the one seam of all requests calls.
"""

import json
import sys
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
MONOTONIC_OFFSET = 123_456.0  # so that mixing time() and monotonic() fails the tests
MINUTE = 60
BUDGET = 90 * MINUTE
REQUEST_TIMEOUT = (5, 60)
PROJECTS_STOPPED = "stopped after 5 consecutive failures, {} projects were not attempted"
PROJECTS_FAILED = "{} projects could not be removed, not counting conflicts"
KEYS_STOPPED = "stopped after 5 consecutive failures, {} keys were not attempted"
KEYS_FAILED = "{} keys could not be removed"


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
        return self.now + MONOTONIC_OFFSET

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
        pytest.param([200, 200, 409, 202], 202, [*PUTS, DELETE, DELETE], [3.5], id="409-is-retried"),
        pytest.param([200, 200, 409, 409, 409], 409, [*PUTS, DELETE, DELETE, DELETE], [3.5, 6.5], id="409-gives-up"),
        pytest.param([200, 200, 401], 401, [*PUTS, DELETE], [], id="401-is-final"),
        pytest.param([200, 200, 403], 403, [*PUTS, DELETE], [], id="403-is-final"),
        pytest.param(
            [200, 500, 409, 200, 202],
            202,
            [*PUTS, DELETE, AUTOMATION_CONFIG, DELETE],
            [3.5],
            id="failed-put-and-409-are-retried",
        ),
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


@pytest.mark.parametrize(
    "scripted,expected_message",
    [
        pytest.param([200, 200, 500, 502, 503], "the last status is 503", id="server-errors"),
        pytest.param([200, 200, 409, 409, 409], "the last status is 409", id="conflicts"),
        pytest.param([200, 200, *[requests.ConnectionError("reset")] * 3], "the last status is 599", id="no-response"),
        pytest.param([200, 200, 500, 500, 202], None, id="removed-on-the-last-attempt"),
        pytest.param([200, 200, 500, 500, 404], None, id="already-gone-on-the-last-attempt"),
        pytest.param([200, 200, 500, 500, 401], None, id="final-failure-on-the-last-attempt"),
        pytest.param([200, 200, 401], None, id="final-failure-without-retries"),
        pytest.param([200, 200, 202], None, id="removed"),
    ],
)
def test_remove_group_by_id_says_when_it_gives_up(monkeypatch, clock, capsys, scripted, expected_message):
    fake_requests(monkeypatch, *scripted)

    qa.remove_group_by_id(FAKE_GROUP_ID)

    output = capsys.readouterr().out
    if expected_message:
        assert f"Giving up on {GROUP_URL} after 3 attempts, {expected_message}\n" in output
    else:
        assert "Giving up" not in output


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
            ["-> 599"],
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


def run_clean_unused_projects(monkeypatch, clock, statuses: list, listing_seconds=0, removal_seconds=0) -> tuple:
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

    problems = qa.clean_unused_projects(FAKE_ORG_ID, clock.monotonic() + BUDGET)

    return removed, problems


@pytest.mark.parametrize(
    "statuses,expected_attempted,expected_output,expected_problems",
    [
        pytest.param([202, 404, 409], 3, ["Removed 1", "Already gone 1", "Kept 1 projects:"], [], id="mixed"),
        pytest.param([404, 404, 202, 202, 202], 5, ["Removed 3", "Already gone 2", "Kept 0 projects:"], [], id="ok"),
        # failures that point at Cloud QA or at the credentials stop the run
        pytest.param(
            [500] * 7,
            5,
            ["Kept 5 projects:"],
            [PROJECTS_STOPPED.format(2), PROJECTS_FAILED.format(5)],
            id="seven-500s-stop-after-five",
        ),
        pytest.param(
            [500] * 5,
            5,
            ["Kept 5 projects:"],
            [PROJECTS_STOPPED.format(0), PROJECTS_FAILED.format(5)],
            id="the-last-five-fail",
        ),
        pytest.param(
            [502, 503, 504, 500, 599, 500],
            5,
            [],
            [PROJECTS_STOPPED.format(1), PROJECTS_FAILED.format(5)],
            id="other-server-errors",
        ),
        pytest.param(
            [429] * 7, 5, [], [PROJECTS_STOPPED.format(2), PROJECTS_FAILED.format(5)], id="seven-429s-stop-after-five"
        ),
        pytest.param(
            [401] * 7, 5, [], [PROJECTS_STOPPED.format(2), PROJECTS_FAILED.format(5)], id="seven-401s-stop-after-five"
        ),
        pytest.param(
            [403] * 7, 5, [], [PROJECTS_STOPPED.format(2), PROJECTS_FAILED.format(5)], id="seven-403s-stop-after-five"
        ),
        pytest.param(
            [500] * 4 + [202] + [500] * 4,
            9,
            ["Removed 1", "Kept 8 projects:"],
            [PROJECTS_FAILED.format(8)],
            id="202-resets",
        ),
        pytest.param(
            [500] * 4 + [404] + [500] * 4,
            9,
            ["Already gone 1", "Kept 8 projects:"],
            [PROJECTS_FAILED.format(8)],
            id="404-resets",
        ),
        # a conflict is a problem of the project itself: it neither counts nor resets, and it is not reported
        pytest.param(
            [500] * 4 + [409] + [500] * 3,
            6,
            ["Kept 6 projects:"],
            [PROJECTS_STOPPED.format(2), PROJECTS_FAILED.format(5)],
            id="409-does-not-reset",
        ),
        pytest.param([409] * 8, 8, ["Kept 8 projects:"], [], id="409-does-not-count"),
    ],
)
def test_clean_unused_projects(
    monkeypatch, clock, capsys, statuses, expected_attempted, expected_output, expected_problems
):
    removed, problems = run_clean_unused_projects(monkeypatch, clock, statuses)

    output = capsys.readouterr().out
    assert problems == expected_problems
    assert removed == [f"id-{i}" for i in range(expected_attempted)]
    assert f"Not attempted {len(statuses) - expected_attempted} projects." in output
    assert ("Stopping after 5 consecutive failures" in output) == any(
        p.startswith("stopped") for p in expected_problems
    )
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
        pytest.param(90 * MINUTE, 0, 10, id="listing-ends-exactly-at-the-deadline"),
        pytest.param(0, 0, 10, id="within-the-budget"),
    ],
)
def test_clean_unused_projects_time_budget(
    monkeypatch, clock, capsys, listing_seconds, removal_seconds, expected_attempted
):
    listed = 10
    removed, problems = run_clean_unused_projects(monkeypatch, clock, [202] * listed, listing_seconds, removal_seconds)

    output = capsys.readouterr().out
    not_attempted = listed - expected_attempted
    assert problems == (
        [f"stopped because the time budget is exhausted, {not_attempted} projects were not attempted"]
        if not_attempted
        else []
    )
    assert removed == [f"id-{i}" for i in range(expected_attempted)]
    assert f"Not attempted {not_attempted} projects." in output
    assert output.count("Stopping because the time budget is exhausted") == bool(not_attempted)


def test_clean_unused_projects_shuffles_the_projects(monkeypatch, clock):
    monkeypatch.setattr(qa.random, "shuffle", lambda items: items.reverse())

    removed, _ = run_clean_unused_projects(monkeypatch, clock, [202] * 3)

    assert removed == ["id-2", "id-1", "id-0"]


def test_clean_unused_projects_lists_the_old_projects_and_summarizes(monkeypatch, clock, capsys):
    listing_calls = []

    def fake_listing(*args, **kwargs):
        listing_calls.append((args, kwargs))
        return fake_projects(1), [{"id": "newer-1"}, {"id": "newer-2"}]

    monkeypatch.setattr(qa, "get_projects_older_than", fake_listing)
    monkeypatch.setattr(qa, "remove_group_by_id", lambda group_id: response(409))

    problems = qa.clean_unused_projects(FAKE_ORG_ID, clock.monotonic() + BUDGET)

    output = capsys.readouterr().out
    assert problems == []
    assert listing_calls == [((FAKE_ORG_ID,), {"minutes_interval": 140})]
    assert "Ignored 2 projects because they are too new." in output
    assert "\ta-1-0\tid-0\n" in output


def test_clean_unused_keys_keeps_the_keys_in_use_and_continues_after_a_failure(monkeypatch, clock, capsys):
    monkeypatch.setenv(qa.USER_OWNER, "User")  # the public key of the owner is compared in lower case
    keys = [
        {"id": "own", "publicKey": "user", "desc": "1"},
        {"id": "evg", "publicKey": "a", "desc": "EVG_KEY"},
        {"id": "not-delete", "publicKey": "b", "desc": "NOT_DELETE_1"},
        {"id": "old-1", "publicKey": "c", "desc": "1"},
        {"id": "failing-1", "publicKey": "d", "desc": "2"},
        {"id": "old-2", "publicKey": "e", "desc": "3"},
        {"id": "failing-2", "publicKey": "f", "desc": "4"},
    ]
    listing_calls = []
    deleted = []

    def fake_delete(org_id, key_id):
        if key_id.startswith("failing"):
            raise Exception("boom")
        deleted.append((org_id, key_id))

    monkeypatch.setattr(
        qa, "get_keys_older_than", lambda *args, **kwargs: listing_calls.append((args, kwargs)) or (keys, [{}])
    )
    monkeypatch.setattr(qa, "delete_api_key", fake_delete)

    problems = qa.clean_unused_keys(FAKE_ORG_ID, clock.monotonic() + BUDGET)

    output = capsys.readouterr().out
    assert problems == [KEYS_FAILED.format(2)]
    assert listing_calls == [((FAKE_ORG_ID,), {"minutes_interval": 140})]
    assert deleted == [(FAKE_ORG_ID, "old-1"), (FAKE_ORG_ID, "old-2")]
    assert "Got an exception trying to remove the key boom" in output
    assert "Ignored 1 keys because they are too new." in output
    assert "Removed 2 keys." in output
    assert "Not attempted 0 keys." in output
    assert "Kept 5:" in output


def run_clean_unused_keys(monkeypatch, clock, outcomes: str, deadline_in=BUDGET, removal_seconds=0) -> tuple:
    """The outcome of each key: o = removed, f = the removal fails, k = the key in use is kept."""
    keys = [
        {"id": f"key-{i}", "publicKey": "user" if o == "k" else f"public-{i}", "desc": "1"}
        for i, o in enumerate(outcomes)
    ]
    attempted = []

    def fake_delete(org_id, key_id):
        attempted.append(key_id)
        clock.now += removal_seconds
        if outcomes[int(key_id.split("-")[1])] == "f":
            raise Exception("boom")

    monkeypatch.setattr(qa, "get_keys_older_than", lambda *args, **kwargs: (keys, []))
    monkeypatch.setattr(qa, "delete_api_key", fake_delete)

    problems = qa.clean_unused_keys(FAKE_ORG_ID, clock.monotonic() + deadline_in)

    return attempted, problems


@pytest.mark.parametrize(
    "outcomes,expected_attempted,expected_not_attempted,expected_problems",
    [
        pytest.param("o" * 8, 8, 0, [], id="all-removed"),
        pytest.param("okk", 1, 0, [], id="kept-keys-are-not-problems"),
        pytest.param(
            "f" * 7, 5, 2, [KEYS_STOPPED.format(2), KEYS_FAILED.format(5)], id="seven-failures-stop-after-five"
        ),
        pytest.param("f" * 5, 5, 0, [KEYS_STOPPED.format(0), KEYS_FAILED.format(5)], id="the-last-five-fail"),
        pytest.param("ffffoffff", 9, 0, [KEYS_FAILED.format(8)], id="a-removal-resets"),
        pytest.param(
            "ffffkfff",
            5,
            2,
            [KEYS_STOPPED.format(2), KEYS_FAILED.format(5)],
            id="the-key-in-use-neither-counts-nor-resets",
        ),
    ],
)
def test_clean_unused_keys_stops_after_consecutive_failures(
    monkeypatch, clock, capsys, outcomes, expected_attempted, expected_not_attempted, expected_problems
):
    attempted, problems = run_clean_unused_keys(monkeypatch, clock, outcomes)

    output = capsys.readouterr().out
    assert problems == expected_problems
    assert len(attempted) == expected_attempted
    assert f"Not attempted {expected_not_attempted} keys." in output
    assert ("Stopping after 5 consecutive failures" in output) == any(
        p.startswith("stopped") for p in expected_problems
    )


@pytest.mark.parametrize(
    "deadline_in,removal_seconds,expected_attempted",
    [
        # the keys start at 0, 40 and 80 minutes
        pytest.param(BUDGET, 40 * MINUTE, 3, id="removals-use-up-the-budget"),
        pytest.param(BUDGET, 0, 4, id="within-the-budget"),
        pytest.param(0, 0, 4, id="deadline-is-now"),
        pytest.param(-1, 0, 0, id="the-deadline-has-passed"),
    ],
)
def test_clean_unused_keys_time_budget(monkeypatch, clock, capsys, deadline_in, removal_seconds, expected_attempted):
    listed = 4
    attempted, problems = run_clean_unused_keys(monkeypatch, clock, "o" * listed, deadline_in, removal_seconds)

    output = capsys.readouterr().out
    not_attempted = listed - expected_attempted
    assert problems == (
        [f"stopped because the time budget is exhausted, {not_attempted} keys were not attempted"]
        if not_attempted
        else []
    )
    assert attempted == [f"key-{i}" for i in range(expected_attempted)]
    assert f"Not attempted {not_attempted} keys." in output
    assert output.count("Stopping because the time budget is exhausted") == bool(not_attempted)


def test_unconfigure_all_cleans_keys_even_when_the_project_cleanup_fails(monkeypatch, clock):
    cleanups = Mock()
    cleanups.projects.side_effect = Exception("listing failed")
    cleanups.keys.return_value = []
    monkeypatch.setattr(qa, "read_env_file", Mock(side_effect=FileNotFoundError))
    monkeypatch.setattr(qa, "read_namespace_from_file", Mock(side_effect=FileNotFoundError))
    monkeypatch.setattr(qa, "clean_unused_projects", cleanups.projects)
    monkeypatch.setattr(qa, "clean_unused_keys", cleanups.keys)

    with pytest.raises(Exception, match="listing failed"):
        qa.unconfigure_all()

    deadline = clock.monotonic() + BUDGET
    assert cleanups.mock_calls == [call.projects(FAKE_ORG_ID, deadline), call.keys(FAKE_ORG_ID, deadline)]


def test_unconfigure_all_cleans_projects_and_keys_when_removing_its_own_key_fails(monkeypatch, clock):
    calls = Mock()
    calls.own_key.side_effect = Exception("boom")
    calls.projects.return_value = []
    calls.keys.return_value = []
    monkeypatch.setattr(qa, "read_env_file", lambda: {"OM_KEY_ID": "own-key"})
    monkeypatch.setattr(qa, "read_namespace_from_file", Mock(side_effect=FileNotFoundError))
    monkeypatch.setattr(qa, "delete_api_key", calls.own_key)
    monkeypatch.setattr(qa, "clean_unused_projects", calls.projects)
    monkeypatch.setattr(qa, "clean_unused_keys", calls.keys)

    assert qa.unconfigure_all() == []

    deadline = clock.monotonic() + BUDGET
    assert calls.mock_calls == [
        call.own_key(FAKE_ORG_ID, "own-key"),
        call.projects(FAKE_ORG_ID, deadline),
        call.keys(FAKE_ORG_ID, deadline),
    ]


@pytest.mark.parametrize("removal_error", [None, Exception("boom")], ids=["removed", "fails"])
def test_unconfigure_all_removes_the_namespace_group_and_still_cleans(monkeypatch, clock, removal_error):
    calls = Mock()
    calls.group.side_effect = removal_error
    calls.projects.return_value = []
    calls.keys.return_value = []
    monkeypatch.setattr(qa, "read_env_file", Mock(side_effect=FileNotFoundError))
    monkeypatch.setattr(qa, "read_namespace_from_file", lambda: "a-1-x")
    monkeypatch.setattr(qa, "remove_group_by_name", calls.group)
    monkeypatch.setattr(qa, "clean_unused_projects", calls.projects)
    monkeypatch.setattr(qa, "clean_unused_keys", calls.keys)

    assert qa.unconfigure_all() == []

    deadline = clock.monotonic() + BUDGET
    assert calls.mock_calls == [
        call.group("a-1-x"),
        call.projects(FAKE_ORG_ID, deadline),
        call.keys(FAKE_ORG_ID, deadline),
    ]


def test_unconfigure_all_returns_the_problems_of_the_projects_and_of_the_keys(monkeypatch, clock):
    monkeypatch.setattr(qa, "read_env_file", Mock(side_effect=FileNotFoundError))
    monkeypatch.setattr(qa, "read_namespace_from_file", Mock(side_effect=FileNotFoundError))
    monkeypatch.setattr(qa, "clean_unused_projects", Mock(return_value=["projects problem"]))
    monkeypatch.setattr(qa, "clean_unused_keys", Mock(return_value=["keys problem"]))

    assert qa.unconfigure_all() == ["projects problem", "keys problem"]


@pytest.fixture
def argv(monkeypatch):
    """Sets the command of main() and the environment it requires."""

    def set_command(command: str):
        monkeypatch.setattr(sys, "argv", ["setup_cloud_qa.py", command])
        monkeypatch.setenv(qa.ENV_FILE, "env-file")
        monkeypatch.setenv(qa.NAMESPACE_FILE, "namespace-file")
        monkeypatch.setenv(qa.OPS_MANAGER_VERSION, "cloud_qa")

    return set_command


FOREVER = float("inf")
BOOM = Mock(side_effect=Exception("boom"))


@pytest.mark.parametrize(
    "stubbed,send",
    [
        pytest.param(
            {"get_projects_older_than": BOOM},
            lambda: qa.clean_unused_projects(FAKE_ORG_ID, FOREVER),
            id="clean_unused_projects",
        ),
        pytest.param(
            {"get_keys_older_than": BOOM}, lambda: qa.clean_unused_keys(FAKE_ORG_ID, FOREVER), id="clean_unused_keys"
        ),
        pytest.param(
            {"clean_unused_projects": Mock(return_value=[]), "clean_unused_keys": BOOM},
            qa.unconfigure_all,
            id="unconfigure_all",
        ),
        pytest.param({"unconfigure_all": BOOM}, qa.main, id="main"),
    ],
)
def test_a_failed_listing_is_not_swallowed(monkeypatch, clock, argv, stubbed, send):
    argv("delete_all")
    monkeypatch.setattr(qa, "read_env_file", Mock(side_effect=FileNotFoundError))
    monkeypatch.setattr(qa, "read_namespace_from_file", Mock(side_effect=FileNotFoundError))
    for name, stub in stubbed.items():
        monkeypatch.setattr(qa, name, stub)

    with pytest.raises(Exception, match="boom"):
        send()


@pytest.mark.parametrize(
    "problems_per_org,expected_exit,expected_report",
    [
        pytest.param([[]], 0, "", id="no-problems"),
        pytest.param([["a problem"]], 1, "Cloud QA cleanup problems:\n  a problem\n", id="a-problem"),
        pytest.param(
            [["first"], [], ["second", "third"]],
            1,
            "Cloud QA cleanup problems:\n  first\n  second\n  third\n",
            id="problems-of-every-org-are-reported-once-after-all-orgs-are-cleaned",
        ),
    ],
)
def test_main_delete_all_fails_when_the_cleanup_reports_problems(
    monkeypatch, capsys, argv, problems_per_org, expected_exit, expected_report
):
    argv("delete_all")
    orgs = [(f"org-{i}", f"user-{i}", f"key-{i}") for i in range(len(problems_per_org))]
    seen = []

    def fake_unconfigure_all():
        seen.append((qa.ORG_ID, qa.USER_OWNER, qa.APIKEY_OWNER))
        return problems_per_org[len(seen) - 1]

    # main() assigns these globals: monkeypatch restores them
    for name in ("ORG_ID", "USER_OWNER", "APIKEY_OWNER"):
        monkeypatch.setattr(qa, name, "unset")
    monkeypatch.setattr(qa, "ORG_IDS", [org for org, _, _ in orgs])
    monkeypatch.setattr(qa, "USER_OWNERS", [user for _, user, _ in orgs])
    monkeypatch.setattr(qa, "APIKEY_OWNERS", [key for _, _, key in orgs])
    monkeypatch.setattr(qa, "unconfigure_all", fake_unconfigure_all)

    assert qa.main() == expected_exit

    output = capsys.readouterr().out
    assert seen == orgs
    assert output.count("Cloud QA cleanup problems:") == bool(expected_report)
    assert output.endswith(expected_report)


@pytest.mark.parametrize(
    "command,expected_called,expected_exit",
    [
        pytest.param("create", "configure", 0, id="create"),
        pytest.param("delete", "unconfigure_from_used_project", 0, id="delete"),
        pytest.param("unknown", None, 1, id="unknown-command"),
    ],
)
def test_main_runs_the_command(monkeypatch, argv, command, expected_called, expected_exit):
    argv(command)
    commands = {name: Mock() for name in ("configure", "unconfigure_from_used_project")}
    for name, stub in commands.items():
        monkeypatch.setattr(qa, name, stub)

    assert qa.main() == expected_exit

    assert [name for name, stub in commands.items() if stub.call_count] == (
        [expected_called] if expected_called else []
    )


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
        with pytest.raises(Exception, match=f"Could not remove the Programmatic API Key.*{status_code}"):
            qa.delete_api_key(FAKE_ORG_ID, "key-id")
    else:
        assert qa.delete_api_key(FAKE_ORG_ID, "key-id").status_code == status_code
    assert fake.calls == [("DELETE", f"{FAKE_BASE_URL}/api/public/v1.0/orgs/{FAKE_ORG_ID}/apiKeys/key-id")]
    assert (fake.kwargs[0]["auth"].username, fake.kwargs[0]["auth"].password) == ("user", "secret")


@pytest.mark.parametrize(
    "send,scripted",
    [
        pytest.param(lambda: qa.create_api_key(FAKE_ORG_ID, "description"), [(200, "{}")], id="create_api_key"),
        pytest.param(lambda: qa.create_group(FAKE_ORG_ID, "a-1-x"), [200], id="create_group"),
        pytest.param(lambda: qa.whitelist_key(FAKE_ORG_ID, "key-id"), [200], id="whitelist_key"),
        pytest.param(lambda: qa.get_group_id_by_name("a-1-x"), [(200, '{"id": "group"}')], id="get_group_id_by_name"),
        pytest.param(lambda: qa.delete_api_key(FAKE_ORG_ID, "key-id"), [204], id="delete_api_key"),
        pytest.param(lambda: qa.get_projects_older_than(FAKE_ORG_ID), [(200, '{"results": []}')], id="projects"),
        pytest.param(lambda: qa.get_keys_older_than(FAKE_ORG_ID), [(200, '{"results": []}')], id="keys"),
        pytest.param(lambda: qa.remove_group_by_id(FAKE_GROUP_ID), [200, 200, 202], id="remove_group_by_id"),
    ],
)
def test_every_request_has_a_timeout(monkeypatch, clock, send, scripted):
    fake = fake_requests(monkeypatch, *scripted)

    send()

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

import json

import pytest
from wt_ctl.domains.evg import EvgDomain, parse_hosts, pick_host
from wt_ctl.errors import WtCtlError

HOSTS = [
    {"id": "i-0aaa", "name": "it-d", "status": "terminated", "host_name": "old.example.com"},
    {"id": "i-0bbb", "name": "it-d", "status": "running", "host_name": "new.example.com"},
]


def test_a_running_host_is_picked_over_a_terminated_one_of_the_same_name():
    assert pick_host(HOSTS, "it-d")["id"] == "i-0bbb"


def test_a_host_that_only_exists_terminated_is_not_picked():
    assert pick_host(HOSTS[:1], "it-d") is None


def test_the_status_line_before_the_json_is_skipped():
    assert parse_hosts("Evergreen is slow today\n" + json.dumps(HOSTS)) == HOSTS


def test_output_without_a_json_list_is_an_error():
    with pytest.raises(WtCtlError, match="JSON list"):
        parse_hosts("login required")


def fail(*args):
    raise WtCtlError("no")


def test_an_unreadable_tag_is_decided_by_the_legacy_marker_or_is_an_error(monkeypatch):
    domain = EvgDomain(None)
    monkeypatch.setattr(domain, "spawned_by_wtctl", fail)
    assert domain.owns(HOSTS[1], "LEGACY=1")
    with pytest.raises(WtCtlError, match="cannot tell"):
        domain.owns(HOSTS[1], "")


@pytest.mark.parametrize("status", ["Terminated", "quarantined", "decommissioned", "running", None])
def test_a_failed_terminate_is_fine_only_when_evergreen_reports_the_host_terminated(monkeypatch, status):
    domain = EvgDomain(None)
    monkeypatch.setattr(domain, "terminate", fail)
    monkeypatch.setattr(domain, "host_field", fail if status is None else lambda host_id, field: status)
    if status == "Terminated":
        domain.end_host("it-d", "i-0bbb")
    else:
        with pytest.raises(WtCtlError, match="terminate --host i-0bbb"):
            domain.end_host("it-d", "i-0bbb")


@pytest.mark.parametrize("spawned,fails", [("i-0bbb", False), (None, False), ("i-0bbb", True)])
def test_a_host_that_does_not_run_is_terminated_by_id_only_when_this_create_spawned_it(monkeypatch, spawned, fails):
    domain, ended = EvgDomain(None), []

    def wait_for(what, find, timeout_s):
        find([{**HOSTS[1], "status": "starting"}])
        raise WtCtlError("timed out after 600s")

    monkeypatch.setattr(domain, "find", lambda name: None if spawned else HOSTS[1])
    monkeypatch.setattr(domain, "spawn", lambda *args: spawned)
    monkeypatch.setattr(domain, "wait_for", wait_for)
    monkeypatch.setattr(domain, "terminate", fail if fails else ended.append)
    with pytest.raises(WtCtlError) as error:
        domain.prepare_host(None, "it-d", "distro", "region", 24, None)
    assert ended == ([spawned] if spawned and not fails else [])
    assert "last known status of host it-d: starting" in str(error.value)
    assert (f"terminate --host {spawned}" in str(error.value)) == fails

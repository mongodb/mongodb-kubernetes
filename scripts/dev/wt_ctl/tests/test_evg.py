import json

import pytest
from wt_ctl.domains.evg import parse_hosts, pick_host
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

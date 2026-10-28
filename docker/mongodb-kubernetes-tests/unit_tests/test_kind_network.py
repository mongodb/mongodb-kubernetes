import pytest
from tests.kind_network import kind_lb_ip


def test_address_is_offset_from_the_first_address_of_the_pool(monkeypatch):
    monkeypatch.setenv("KIND_METALLB_RANGE_CLUSTER_1", "172.18.255.210-172.18.255.219")
    assert kind_lb_ip("CLUSTER_1", 1) == "172.18.255.211"


def test_unset_pool_is_reported_by_its_variable_name(monkeypatch):
    monkeypatch.delenv("KIND_METALLB_RANGE_CLUSTER_1", raising=False)
    with pytest.raises(RuntimeError, match="KIND_METALLB_RANGE_CLUSTER_1"):
        kind_lb_ip("CLUSTER_1")


def test_offset_past_the_end_of_the_pool_is_rejected(monkeypatch):
    monkeypatch.setenv("KIND_METALLB_RANGE_CLUSTER_1", "172.18.255.210-172.18.255.219")
    with pytest.raises(ValueError):
        kind_lb_ip("CLUSTER_1", 10)

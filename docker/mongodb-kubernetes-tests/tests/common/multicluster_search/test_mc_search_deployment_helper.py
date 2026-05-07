"""Unit tests for MCSearchDeploymentHelper (mocked clients)."""

from unittest.mock import MagicMock

import pytest
from tests.common.multicluster_search.mc_search_deployment_helper import MCSearchDeploymentHelper


def test_helper_records_member_cluster_clients():
    member_clients = {"cluster-a": MagicMock(), "cluster-b": MagicMock()}
    helper = MCSearchDeploymentHelper(
        namespace="ns",
        mdb_resource_name="mdb-multi",
        mdbs_resource_name="mdb-search",
        member_cluster_clients=member_clients,
    )

    assert helper.namespace == "ns"
    assert helper.member_cluster_names() == ["cluster-a", "cluster-b"]
    assert helper.cluster_index("cluster-a") == 0
    assert helper.cluster_index("cluster-b") == 1


def test_helper_proxy_svc_fqdn_uses_cluster_index():
    helper = MCSearchDeploymentHelper(
        namespace="test-ns",
        mdb_resource_name="mdb",
        mdbs_resource_name="mdb-search",
        member_cluster_clients={"a": MagicMock(), "b": MagicMock()},
    )

    assert helper.proxy_svc_fqdn("a") == "mdb-search-search-0-proxy-svc.test-ns.svc.cluster.local"
    assert helper.proxy_svc_fqdn("b") == "mdb-search-search-1-proxy-svc.test-ns.svc.cluster.local"


def test_helper_unknown_cluster_raises():
    helper = MCSearchDeploymentHelper(
        namespace="ns",
        mdb_resource_name="m",
        mdbs_resource_name="s",
        member_cluster_clients={"a": MagicMock()},
    )
    with pytest.raises(KeyError):
        helper.cluster_index("nope")

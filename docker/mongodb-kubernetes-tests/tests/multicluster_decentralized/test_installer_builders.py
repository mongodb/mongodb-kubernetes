"""Unit tests over the decentralized-installer builders. Pure functions only: no cluster, no
live Ops Manager."""

from unittest import mock

import pytest
from kubetester.kubetester import KubernetesTester

from tests.multicluster_decentralized.installer import (
    build_agent_api_key_secret,
    build_credential_secret,
    build_decentralized_settings,
    build_member_cluster_cr,
    build_om_ca_config_map,
    build_om_credentials_secret,
    build_peer_kubeconfig,
    build_peer_role,
    build_peer_role_binding,
    build_peer_service_account,
    build_peer_token_secret,
    build_project_config_map,
    peers_of,
)


def test_project_config_map_shape():
    cm = build_project_config_map("mdb-ns", "http://om:8080", "org1", "mdb-ns")

    assert cm["kind"] == "ConfigMap"
    assert cm["metadata"] == {"name": "my-project", "namespace": "mdb-ns"}
    assert cm["data"] == {"baseUrl": "http://om:8080", "orgId": "org1", "projectName": "mdb-ns"}


def test_om_ca_config_map_shape():
    cm = build_om_ca_config_map("mdb-ns", "OM CA PEM")

    # The single key must be mms-ca.crt: the database pods mount the ConfigMap named by the
    # project's sslMMSCAConfigMap and the agent reads exactly that entry.
    assert cm["metadata"] == {"name": "om-ca", "namespace": "mdb-ns"}
    assert cm["data"] == {"mms-ca.crt": "OM CA PEM"}


def test_project_config_map_references_the_om_ca_only_when_set():
    with_ca = build_project_config_map("mdb-ns", "http://om:8080", "org1", "mdb-ns", ssl_mms_ca_configmap="om-ca")
    without_ca = build_project_config_map("mdb-ns", "http://om:8080", "org1", "mdb-ns")

    assert with_ca["data"]["sslMMSCAConfigMap"] == "om-ca"
    assert "sslMMSCAConfigMap" not in without_ca["data"]


def test_om_credentials_secret_shape():
    secret = build_om_credentials_secret("mdb-ns", "jane.doe@example.com", "api-key")

    assert secret["kind"] == "Secret"
    assert secret["metadata"] == {"name": "my-credentials", "namespace": "mdb-ns"}
    assert secret["stringData"] == {"user": "jane.doe@example.com", "publicApiKey": "api-key"}


def test_agent_api_key_secret_shape():
    secret = build_agent_api_key_secret("mdb-ns", "proj123", "agent-key")

    # The name must match agents.ApiKeySecretName (<projectID>-group-secret) and the single key
    # must be agentApiKey: the member controller reads exactly this Secret to run the agents.
    assert secret["metadata"] == {"name": "proj123-group-secret", "namespace": "mdb-ns"}
    assert secret["stringData"] == {"agentApiKey": "agent-key"}


def test_peer_role_equals_the_frozen_contract_verbatim():
    """The cross-track contract, restated verbatim: leases get/create/update, mongodbdirectives +
    mongodbdirectives/status get/list/watch/create/update, NOTHING else. Full equality (not
    containment) is the point — any extra rule, resource or verb fails this test. The printed
    matrix is the artifact M4 puts side by side with today's full-CRUD hub-and-spoke member Role.
    """
    role = build_peer_role("kind-e2e-cluster-1", "mdb-ns")

    assert role["rules"] == [
        {
            "apiGroups": ["coordination.k8s.io"],
            "resources": ["leases"],
            "verbs": ["get", "create", "update"],
        },
        {
            "apiGroups": ["operator.mongodb.com"],
            "resources": ["mongodbdirectives", "mongodbdirectives/status"],
            "verbs": ["get", "list", "watch", "create", "update"],
        },
    ]

    print("\nCross-cluster credential matrix (everything a peer token can do):")
    for rule in role["rules"]:
        for resource in rule["resources"]:
            print(f"  {rule['apiGroups'][0]}/{resource}: {', '.join(rule['verbs'])}")


def test_peer_identity_shapes():
    sa = build_peer_service_account("kind-e2e-cluster-1", "mdb-ns")
    token = build_peer_token_secret("kind-e2e-cluster-1", "mdb-ns")
    binding = build_peer_role_binding("kind-e2e-cluster-1", "mdb-ns")

    assert sa["metadata"] == {"name": "mck-member-kind-e2e-cluster-1-sa", "namespace": "mdb-ns"}
    # The token Secret's type and service-account.name annotation are what make Kubernetes mint a
    # long-lived token into it — the shape memberregistration.Generate polls for.
    assert token["type"] == "kubernetes.io/service-account-token"
    assert token["metadata"]["name"] == "mck-member-kind-e2e-cluster-1-token"
    assert token["metadata"]["annotations"] == {"kubernetes.io/service-account.name": "mck-member-kind-e2e-cluster-1-sa"}
    assert binding["roleRef"]["name"] == "mck-member-kind-e2e-cluster-1-peer-role"
    assert binding["subjects"] == [
        {"kind": "ServiceAccount", "name": "mck-member-kind-e2e-cluster-1-sa", "namespace": "mdb-ns"}
    ]


def test_peer_registration_shapes():
    kubeconfig = build_peer_kubeconfig("kind-e2e-cluster-2", "https://10.96.0.1", "mdb-ns", "Y2E=", "tok")
    secret = build_credential_secret("kind-e2e-cluster-2", "mdb-ns", "kubeconfig-yaml")
    cr = build_member_cluster_cr("kind-e2e-cluster-2", "mdb-ns")

    assert kubeconfig["current-context"] == "kind-e2e-cluster-2"
    assert kubeconfig["clusters"] == [
        {
            "name": "kind-e2e-cluster-2",
            "cluster": {"server": "https://10.96.0.1", "certificate-authority-data": "Y2E="},
        }
    ]
    assert kubeconfig["users"] == [{"name": "mck-operator", "user": {"token": "tok"}}]
    assert kubeconfig["contexts"] == [
        {
            "name": "kind-e2e-cluster-2",
            "context": {"cluster": "kind-e2e-cluster-2", "user": "mck-operator", "namespace": "mdb-ns"},
        }
    ]
    # The credential Secret name (mck-credential-<peer>) and key (kubeconfig) plus the CR's
    # credentialSecretRef are the operator's discovery contract (pkg/resourcenames).
    assert secret["metadata"]["name"] == "mck-credential-kind-e2e-cluster-2"
    assert secret["stringData"] == {"kubeconfig": "kubeconfig-yaml"}
    assert cr["apiVersion"] == "operator.mongodb.com/v1"
    assert cr["kind"] == "MemberCluster"
    assert cr["metadata"] == {"name": "kind-e2e-cluster-2", "namespace": "mdb-ns"}
    assert cr["spec"] == {
        "clusterName": "kind-e2e-cluster-2",
        "credentialSecretRef": {"name": "mck-credential-kind-e2e-cluster-2"},
    }


def test_peers_of_excludes_self():
    clusters = ["kind-e2e-cluster-1", "kind-e2e-cluster-2", "kind-e2e-cluster-3"]

    assert peers_of("kind-e2e-cluster-2", clusters) == ["kind-e2e-cluster-1", "kind-e2e-cluster-3"]


class TestBuildDecentralizedSettings:
    """The fixture-swap path's settings builder (tests/conftest.py, DECENTRALIZED_E2E=true)."""

    def test_overrides_pinned_by_the_harness(self, monkeypatch):
        monkeypatch.setenv("OM_HOST", "http://om:8080")
        monkeypatch.setenv("OM_USER", "jane.doe@example.com")
        monkeypatch.setenv("OM_API_KEY", "api-key")
        monkeypatch.setenv("OM_ORGID", "org1")
        # The harness always overrides the leader to the status-read cluster, even if this is set.
        monkeypatch.setenv("OPERATOR_LEADER_CLUSTER_NAME", "kind-e2e-cluster-3")
        members = ["kind-e2e-cluster-1", "kind-e2e-cluster-2"]

        settings = build_decentralized_settings("mdb-ns", members, "kind-e2e-cluster-1")

        assert settings.namespace == "mdb-ns"
        assert settings.project_name == "mdb-ns"
        assert settings.clusters == members
        assert settings.include_workload_cr is False
        assert settings.forced_leader_cluster == "kind-e2e-cluster-1"
        # OM inputs still flow through from the environment untouched.
        assert settings.om_base_url == "http://om:8080"
        assert settings.om_user == "jane.doe@example.com"
        assert settings.om_api_key == "api-key"
        assert settings.om_org_id == "org1"

    def test_passes_through_operator_config_extra_spec(self, monkeypatch):
        monkeypatch.setenv("OM_HOST", "http://om:8080")
        extra_spec = {"foo": "bar"}

        settings = build_decentralized_settings(
            "mdb-ns", ["kind-e2e-cluster-1"], "kind-e2e-cluster-1", operator_config_extra_spec=extra_spec
        )

        assert settings.operator_config_extra_spec == extra_spec

    def test_rejects_a_central_cluster_that_is_not_a_member(self, monkeypatch):
        monkeypatch.setenv("OM_HOST", "http://om:8080")

        with pytest.raises(ValueError):
            build_decentralized_settings("mdb-ns", ["kind-e2e-cluster-1"], "kind-e2e-cluster-9")


class TestEnsureGroupWithAgentKey:
    """ensure_group_with_agent_key against a mocked OM API."""

    def test_creating_a_group_returns_the_minted_agent_key(self, monkeypatch):
        monkeypatch.setenv("OM_HOST", "http://om:8080")
        response = mock.Mock()
        response.json.return_value = {"id": "proj123", "agentApiKey": "minted-key"}

        with (
            mock.patch.object(KubernetesTester, "get_om_group_id", side_effect=Exception("not found")),
            mock.patch.object(KubernetesTester, "om_request", return_value=response) as om_request,
        ):
            assert KubernetesTester.ensure_group_with_agent_key("org1", "mdb-ns") == ("proj123", "minted-key")

        om_request.assert_called_once_with(
            "post", "http://om:8080/api/public/v1.0/groups", {"name": "mdb-ns", "orgId": "org1"}
        )

    def test_existing_group_generates_a_fresh_agent_key(self, monkeypatch):
        monkeypatch.setenv("OM_HOST", "http://om:8080")
        response = mock.Mock()
        response.json.return_value = {"key": "fresh-key"}

        with (
            mock.patch.object(KubernetesTester, "get_om_group_id", return_value="proj123"),
            mock.patch.object(KubernetesTester, "om_request", return_value=response) as om_request,
        ):
            assert KubernetesTester.ensure_group_with_agent_key("org1", "mdb-ns") == ("proj123", "fresh-key")

        om_request.assert_called_once_with(
            "post",
            "http://om:8080/api/public/v1.0/groups/proj123/agentapikeys",
            {"desc": "Agent API key for Kubernetes"},
        )

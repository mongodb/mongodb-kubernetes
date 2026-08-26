"""Unit tests over the decentralized-installer builders. Pure functions only: no cluster, no
live Ops Manager."""

from unittest import mock

from kubetester.kubetester import KubernetesTester

from tests.multicluster_decentralized.installer import (
    build_agent_api_key_secret,
    build_om_credentials_secret,
    build_project_config_map,
)


def test_project_config_map_shape():
    cm = build_project_config_map("mdb-ns", "http://om:8080", "org1", "mdb-ns")

    assert cm["kind"] == "ConfigMap"
    assert cm["metadata"] == {"name": "my-project", "namespace": "mdb-ns"}
    assert cm["data"] == {"baseUrl": "http://om:8080", "orgId": "org1", "projectName": "mdb-ns"}


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

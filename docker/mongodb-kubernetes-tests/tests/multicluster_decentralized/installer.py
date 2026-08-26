"""
Installer for the decentralized multi-cluster POC (CLOUDP-420273): one operator per cluster, no
central cluster. Every cluster gets the same inputs (namespace, CRDs, OM project artifacts, the
workload CR) plus, for each of its two peers, a narrowly scoped token-kubeconfig credential and a
MemberCluster CR.

This module holds pure builders that return Kubernetes objects as dicts, and the dry-run entry
point that renders every object to a directory. Wiring to per-cluster clients (the live path)
lives in conftest.py and the smoke test.
"""

PROJECT_CONFIGMAP_NAME = "my-project"
OM_CREDENTIALS_SECRET_NAME = "my-credentials"


# --- Ops Manager artifacts, created identically on every cluster ---
#
# The project and its agent API key are pre-provisioned by the installer (see
# KubernetesTester.ensure_group_with_agent_key): operators only ever read OM credentials, they
# never mint them.


def build_project_config_map(namespace: str, base_url: str, org_id: str, project_name: str) -> dict:
    """The OM project ConfigMap (same shape as scripts/dev/configure_operator.sh renders)."""
    return {
        "apiVersion": "v1",
        "kind": "ConfigMap",
        "metadata": {"name": PROJECT_CONFIGMAP_NAME, "namespace": namespace},
        "data": {"baseUrl": base_url, "orgId": org_id, "projectName": project_name},
    }


def build_om_credentials_secret(namespace: str, user: str, public_api_key: str) -> dict:
    """The OM API credentials Secret (the user/publicApiKey shape the e2e harness uses)."""
    return {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {"name": OM_CREDENTIALS_SECRET_NAME, "namespace": namespace},
        "type": "Opaque",
        "stringData": {"user": user, "publicApiKey": public_api_key},
    }


def build_agent_api_key_secret(namespace: str, project_id: str, agent_api_key: str) -> dict:
    """The agent API key Secret (<projectID>-group-secret with the single key agentApiKey,
    matching agents.ApiKeySecretName). Normally written by the operator on first reconcile; here
    the installer pre-provisions it so the member controller can hold observably without OM write
    access."""
    return {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {"name": f"{project_id}-group-secret", "namespace": namespace},
        "type": "Opaque",
        "stringData": {"agentApiKey": agent_api_key},
    }

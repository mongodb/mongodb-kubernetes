"""
Installer for the decentralized multi-cluster POC (CLOUDP-420273): one operator per cluster, no
central cluster. Every cluster gets the same inputs (namespace, CRDs, OM project artifacts, the
workload CR) plus, for each of its two peers, a narrowly scoped token-kubeconfig credential and a
MemberCluster CR.

This module holds pure builders that return Kubernetes objects as dicts, and the dry-run entry
point that renders every object to a directory. Wiring to per-cluster clients (the live path)
lives in conftest.py and the smoke test.
"""

from typing import List

PROJECT_CONFIGMAP_NAME = "my-project"
OM_CREDENTIALS_SECRET_NAME = "my-credentials"

# The frozen cross-track RBAC contract: everything one operator may do in a peer cluster.
# leases (coordination.k8s.io) carry the majority-lease election; mongodbdirectives carry
# delivery. list+watch on directives are mandatory — the leader opens a real informer per peer
# cluster. NOTHING else: peers can stall coordination, but can never read data or delete a
# workload. The hub-and-spoke member Role (member-cluster-rbac.yaml / generate-member-resources)
# is deliberately NOT reused here — it grants broad workload management.
PEER_ROLE_RULES = [
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


def peers_of(cluster_name: str, all_clusters: List[str]) -> List[str]:
    return [c for c in all_clusters if c != cluster_name]


# --- Peer identity on each cluster: SA + long-lived token + the contract Role ---
#
# Resource names mirror pkg/resourcenames (mck-member-<cluster>-sa/-token); the Role name gets a
# -peer- segment so it can never be confused with the hub-and-spoke -role-base workload Role.


def build_peer_service_account(cluster_name: str, namespace: str) -> dict:
    """The ServiceAccount on cluster_name that its two peers authenticate as."""
    return {
        "apiVersion": "v1",
        "kind": "ServiceAccount",
        "metadata": {"name": f"mck-member-{cluster_name}-sa", "namespace": namespace},
    }


def build_peer_token_secret(cluster_name: str, namespace: str) -> dict:
    """The long-lived token Secret for the peer ServiceAccount. Kubernetes populates .data.token
    and .data['ca.crt'] asynchronously; the live path polls for them (the same shape
    memberregistration.Generate reads)."""
    return {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {
            "name": f"mck-member-{cluster_name}-token",
            "namespace": namespace,
            "annotations": {"kubernetes.io/service-account.name": f"mck-member-{cluster_name}-sa"},
        },
        "type": "kubernetes.io/service-account-token",
    }


def build_peer_role(cluster_name: str, namespace: str) -> dict:
    return {
        "apiVersion": "rbac.authorization.k8s.io/v1",
        "kind": "Role",
        "metadata": {"name": f"mck-member-{cluster_name}-peer-role", "namespace": namespace},
        "rules": PEER_ROLE_RULES,
    }


def build_peer_role_binding(cluster_name: str, namespace: str) -> dict:
    return {
        "apiVersion": "rbac.authorization.k8s.io/v1",
        "kind": "RoleBinding",
        "metadata": {"name": f"mck-member-{cluster_name}-peer-role-binding", "namespace": namespace},
        "roleRef": {
            "apiGroup": "rbac.authorization.k8s.io",
            "kind": "Role",
            "name": f"mck-member-{cluster_name}-peer-role",
        },
        "subjects": [
            {"kind": "ServiceAccount", "name": f"mck-member-{cluster_name}-sa", "namespace": namespace}
        ],
    }


# --- Peer registration on each consuming cluster: kubeconfig Secret + MemberCluster CR ---
#
# Shapes mirror pkg/kubectl-mongodb/memberregistration/memberregistration.go: a single-context
# bearer-token kubeconfig in a Secret named mck-credential-<peer> under the key 'kubeconfig',
# referenced by a MemberCluster CR. Each cluster registers its TWO PEERS only — the operator
# self-inserts its own cluster's client (main.go).


def build_peer_kubeconfig(cluster_name: str, server_url: str, namespace: str, ca_data: str, token: str) -> dict:
    """A single-context kubeconfig for reaching cluster_name (buildKubeConfig's shape). ca_data
    is base64 (as stored in the token Secret); server_url must be reachable from inside the
    consuming cluster's pods — for kind, https://<default/kubernetes Service clusterIP> of the
    target cluster, routed by the interconnect."""
    return {
        "apiVersion": "v1",
        "kind": "Config",
        "clusters": [
            {"name": cluster_name, "cluster": {"server": server_url, "certificate-authority-data": ca_data}}
        ],
        "users": [{"name": "mck-operator", "user": {"token": token}}],
        "contexts": [
            {"name": cluster_name, "context": {"cluster": cluster_name, "user": "mck-operator", "namespace": namespace}}
        ],
        "current-context": cluster_name,
    }


def build_credential_secret(peer_name: str, namespace: str, kubeconfig_yaml: str) -> dict:
    return {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {"name": f"mck-credential-{peer_name}", "namespace": namespace},
        "type": "Opaque",
        "stringData": {"kubeconfig": kubeconfig_yaml},
    }


def build_member_cluster_cr(peer_name: str, namespace: str) -> dict:
    return {
        "apiVersion": "operator.mongodb.com/v1",
        "kind": "MemberCluster",
        "metadata": {"name": peer_name, "namespace": namespace},
        "spec": {
            "clusterName": peer_name,
            "credentialSecretRef": {"name": f"mck-credential-{peer_name}"},
        },
    }


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

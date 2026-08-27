"""
Installer for the decentralized multi-cluster POC (CLOUDP-420273): one operator per cluster, no
central cluster. Every cluster gets the same inputs (namespace, CRDs, OM project artifacts, the
workload CR) plus, for each of its two peers, a narrowly scoped token-kubeconfig credential and a
MemberCluster CR.

This module holds pure builders that return Kubernetes objects as dicts, and the dry-run entry
point that renders every object to a directory. Wiring to per-cluster clients (the live path)
lives in conftest.py and the smoke test.
"""

import argparse
import base64
import glob
import os
import time
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Tuple

import yaml

PROJECT_CONFIGMAP_NAME = "my-project"
OM_CREDENTIALS_SECRET_NAME = "my-credentials"
DECENTRALIZED_OPERATOR_NAME = "mongodb-kubernetes-operator-decentralized"
WORKLOAD_CR_FIXTURE = os.path.join(os.path.dirname(__file__), "fixtures", "mongodb-multi-decentralized.yaml")

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


# --- Per-cluster inputs shared by every operator ---


def build_namespace(namespace: str) -> dict:
    return {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": namespace}}


def build_operator_config(namespace: str, extra_spec: Optional[dict] = None) -> dict:
    """The OperatorConfig every cluster gets. mongodbdirectives is opt-in only (deliberately
    absent from AllWatchedResources), so the decentralized world must always ask for it."""
    spec = dict(extra_spec or {})
    spec["watchedResources"] = ["mongodbdirectives"]
    return {
        "apiVersion": "operator.mongodb.com/v1",
        "kind": "OperatorConfig",
        "metadata": {"name": "operator-config", "namespace": namespace},
        "spec": spec,
    }


def load_workload_cr(namespace: str, clusters: List[str]) -> dict:
    """The one MongoDBMultiCluster CR applied identically to all clusters. clusterSpecList is
    rewritten from the configured cluster names so the fixture never drifts from the settings."""
    with open(WORKLOAD_CR_FIXTURE) as f:
        cr = yaml.safe_load(f)
    cr["metadata"]["namespace"] = namespace
    cr["spec"]["clusterSpecList"] = [{"clusterName": c, "members": 1} for c in clusters]
    return cr


# --- Settings and the per-cluster plan ---


@dataclass
class InstallerSettings:
    clusters: List[str]
    namespace: str
    om_base_url: str
    om_org_id: str
    om_user: str
    om_api_key: str
    project_name: str
    # Filled by the live path (OM pre-provisioning, token/IP reads); placeholders in dry-run.
    project_id: str = "PLACEHOLDER_PROJECT_ID"
    agent_api_key: str = "PLACEHOLDER_AGENT_API_KEY"
    api_server_urls: Dict[str, str] = field(default_factory=dict)
    # cluster -> (bearer token, base64 ca.crt), read back from the populated token Secrets.
    peer_credentials: Dict[str, Tuple[str, str]] = field(default_factory=dict)
    operator_config_extra_spec: Optional[dict] = None
    # Debugging escape hatch: pins the StaticElector so this cluster always leads, bypassing
    # the quorum lease election. Leave None (the default) to exercise the real election.
    forced_leader_cluster: Optional[str] = None

    def api_server_url(self, cluster_name: str) -> str:
        # The in-pod address of a peer's API server: https://<its default/kubernetes Service
        # clusterIP>, unique per kind cluster and routed by the interconnect (the
        # prepare-multi-cluster overrideKindKubeconfig trick). Placeholder until the live path
        # reads the real Service.
        return self.api_server_urls.get(cluster_name, f"https://placeholder-cluster-ip.{cluster_name}")

    def peer_credential(self, cluster_name: str) -> Tuple[str, str]:
        return self.peer_credentials.get(cluster_name, ("PLACEHOLDER_TOKEN", base64.b64encode(b"PLACEHOLDER_CA").decode()))


def settings_from_env() -> InstallerSettings:
    namespace = os.environ.get("NAMESPACE", "mongodb-test")
    return InstallerSettings(
        clusters=os.environ.get(
            "MEMBER_CLUSTERS", "kind-e2e-cluster-1 kind-e2e-cluster-2 kind-e2e-cluster-3"
        ).split(),
        namespace=namespace,
        om_base_url=os.environ.get("OM_HOST", "https://placeholder-om"),
        om_org_id=os.environ.get("OM_ORGID", ""),
        om_user=os.environ.get("OM_USER", "PLACEHOLDER_OM_USER"),
        om_api_key=os.environ.get("OM_API_KEY", "PLACEHOLDER_OM_API_KEY"),
        project_name=namespace,
        forced_leader_cluster=os.environ.get("OPERATOR_LEADER_CLUSTER_NAME") or None,
    )


def plan_cluster_objects(cluster_name: str, settings: InstallerSettings) -> List[dict]:
    """Everything the installer creates on cluster_name, in apply order. Identical inputs on all
    clusters (namespace, OM artifacts, OperatorConfig, workload CR) plus this cluster's own peer
    identity and the registrations of its two peers."""
    objects = [
        build_namespace(settings.namespace),
        build_peer_service_account(cluster_name, settings.namespace),
        build_peer_token_secret(cluster_name, settings.namespace),
        build_peer_role(cluster_name, settings.namespace),
        build_peer_role_binding(cluster_name, settings.namespace),
        build_project_config_map(settings.namespace, settings.om_base_url, settings.om_org_id, settings.project_name),
        build_om_credentials_secret(settings.namespace, settings.om_user, settings.om_api_key),
        build_agent_api_key_secret(settings.namespace, settings.project_id, settings.agent_api_key),
    ]
    for peer in peers_of(cluster_name, settings.clusters):
        token, ca_data = settings.peer_credential(peer)
        kubeconfig = build_peer_kubeconfig(
            peer, settings.api_server_url(peer), settings.namespace, ca_data, token
        )
        objects.append(build_credential_secret(peer, settings.namespace, yaml.safe_dump(kubeconfig, sort_keys=False)))
        objects.append(build_member_cluster_cr(peer, settings.namespace))
    objects.append(build_operator_config(settings.namespace, settings.operator_config_extra_spec))
    objects.append(load_workload_cr(settings.namespace, settings.clusters))
    return objects


def build_helm_values(cluster_name: str, settings: InstallerSettings) -> Dict[str, str]:
    """The per-cluster helm values on top of the shared installation config. clusterIdentity
    renders OPERATOR_CLUSTER_NAME — the operator's own identity, what it logs at startup."""
    # Imported lazily: kubetester.operator imports tests.conftest internals on module load.
    from kubetester.operator import add_to_custom_env_vars_value

    values = {
        "operator.name": DECENTRALIZED_OPERATOR_NAME,
        "operator.clusterIdentity.clusterName": cluster_name,
        # Three operators in three clusters would need three webhook services; the POC skips
        # webhook registration entirely.
        "operator.webhook.registerConfiguration": "false",
    }
    if settings.forced_leader_cluster:
        add_to_custom_env_vars_value(values, "OPERATOR_LEADER_CLUSTER_NAME", settings.forced_leader_cluster)
    return values


# --- Dry-run: render every object to a directory, apply nothing (the B5 receipt) ---


def render_dry_run(settings: InstallerSettings, out_dir: str) -> Dict[str, Dict[str, int]]:
    """Writes every object the installer would create, per cluster, plus each cluster's helm
    values and the CRD file list. Returns {cluster: {kind: count}}."""
    inventory: Dict[str, Dict[str, int]] = {}
    for cluster in settings.clusters:
        cluster_dir = os.path.join(out_dir, cluster)
        os.makedirs(cluster_dir, exist_ok=True)

        objects = plan_cluster_objects(cluster, settings)
        counts: Dict[str, int] = {}
        for i, obj in enumerate(objects):
            counts[obj["kind"]] = counts.get(obj["kind"], 0) + 1
            filename = f"{i:02d}-{obj['kind'].lower()}-{obj['metadata']['name']}.yaml"
            with open(os.path.join(cluster_dir, filename), "w") as f:
                yaml.safe_dump(obj, f, sort_keys=False)

        with open(os.path.join(cluster_dir, "helm-values.yaml"), "w") as f:
            yaml.safe_dump(build_helm_values(cluster, settings), f, sort_keys=False)

        with open(os.path.join(cluster_dir, "crds-from-chart.txt"), "w") as f:
            f.write("\n".join(os.path.basename(p) for p in crd_files_from_chart()) + "\n")

        inventory[cluster] = counts
    return inventory


def crd_files_from_chart() -> List[str]:
    # DEFAULT_HELM_CHART_PATH points at the local chart both in the test pod and via
    # scripts/dev/contexts locally; the last fallback walks from this file up to the repo root so
    # the dry-run also works from a bare checkout.
    chart_path = os.environ.get("DEFAULT_HELM_CHART_PATH", "")
    if not chart_path:
        chart_path = "helm_chart"
    if not os.path.isdir(chart_path):
        chart_path = os.path.join(os.path.dirname(__file__), "..", "..", "..", "..", "helm_chart")
    return sorted(glob.glob(os.path.join(chart_path, "crds", "*.yaml")))


# --- Live path (B6, gated): apply the plan through per-cluster clients ---


def apply_objects(api_client, objects: List[dict], namespace: str) -> None:
    """kubectl-apply semantics for the plan's objects: built-in kinds go through the kubernetes
    create-or-patch helper, custom resources through CustomObjectsApi."""
    from kubernetes import client
    from kubetester.create_or_replace_from_yaml import create_or_patch_from_dict

    custom_kind_plurals = {
        "MemberCluster": ("operator.mongodb.com", "v1", "memberclusters"),
        "OperatorConfig": ("operator.mongodb.com", "v1", "operatorconfigs"),
        "MongoDBMultiCluster": ("mongodb.com", "v1", "mongodbmulticluster"),
    }
    for obj in objects:
        if obj["kind"] in custom_kind_plurals:
            group, version, plural = custom_kind_plurals[obj["kind"]]
            customv1 = client.CustomObjectsApi(api_client)
            try:
                customv1.create_namespaced_custom_object(group, version, namespace, plural, obj)
            except client.rest.ApiException as e:
                if e.status != 409:
                    raise
                customv1.patch_namespaced_custom_object(
                    group, version, namespace, plural, obj["metadata"]["name"], obj
                )
        else:
            create_or_patch_from_dict(api_client, obj, namespace=namespace)


def apply_crds(cluster_name: str) -> None:
    from kubetester.helm import process_run_and_check

    for crd_file in crd_files_from_chart():
        process_run_and_check(
            f"kubectl --context {cluster_name} apply -f {crd_file}", check=True, capture_output=True, shell=True
        )


def wait_for_peer_token(api_client, cluster_name: str, namespace: str, timeout: int = 120) -> Tuple[str, str]:
    """Polls the peer token Secret until Kubernetes has populated it; returns (token, base64 ca.crt).
    The token goes into the kubeconfig decoded; the CA stays base64 (certificate-authority-data)."""
    from kubernetes import client

    corev1 = client.CoreV1Api(api_client)
    secret_name = f"mck-member-{cluster_name}-token"
    deadline = time.time() + timeout
    while time.time() < deadline:
        secret = corev1.read_namespaced_secret(secret_name, namespace)
        data = secret.data or {}
        if data.get("token") and data.get("ca.crt"):
            return base64.b64decode(data["token"]).decode(), data["ca.crt"]
        time.sleep(1)
    raise Exception(f"Timeout waiting for token Secret {namespace}/{secret_name} on {cluster_name} to be populated")


def read_api_server_url(api_client) -> str:
    from kubernetes import client

    svc = client.CoreV1Api(api_client).read_namespaced_service("kubernetes", "default")
    return f"https://{svc.spec.cluster_ip}"


def install_operator_on_cluster(cluster_name: str, api_client, settings: InstallerSettings, base_helm_args: Dict[str, str]):
    from kubetester.operator import Operator

    os.environ["HELM_KUBECONTEXT"] = cluster_name
    helm_args = dict(base_helm_args)
    helm_args.update(build_helm_values(cluster_name, settings))
    return Operator(
        name=DECENTRALIZED_OPERATOR_NAME,
        namespace=settings.namespace,
        helm_args=helm_args,
        api_client=api_client,
    ).upgrade(multi_cluster=True)


def install_decentralized(cluster_clients: Dict[str, "object"], settings: InstallerSettings, base_helm_args: Dict[str, str]):
    """The full live installation over already-running clusters. Order matters only in the
    obvious ways: CRDs and identities before tokens can be read, everything before the operators
    start reconciling."""
    from kubetester.kubetester import KubernetesTester, build_operator_config_spec_from_test_env

    settings.operator_config_extra_spec = build_operator_config_spec_from_test_env()

    # Namespaces, CRDs and each cluster's own peer identity (SA + token + the contract Role).
    for cluster, api_client in cluster_clients.items():
        apply_crds(cluster)
        identity = plan_cluster_objects(cluster, settings)[:5]
        apply_objects(api_client, identity, settings.namespace)

    # Read back what registration needs: populated tokens and in-pod API server addresses.
    for cluster, api_client in cluster_clients.items():
        settings.peer_credentials[cluster] = wait_for_peer_token(api_client, cluster, settings.namespace)
        settings.api_server_urls[cluster] = read_api_server_url(api_client)

    # OM pre-provisioning: project + agent key, once, shared by all clusters.
    settings.project_id, settings.agent_api_key = KubernetesTester.ensure_group_with_agent_key(
        settings.om_org_id, settings.project_name
    )

    # The complete plan everywhere (idempotent re-apply of the identity part included), then one
    # operator per cluster.
    operators = {}
    for cluster, api_client in cluster_clients.items():
        apply_objects(api_client, plan_cluster_objects(cluster, settings), settings.namespace)
    for cluster, api_client in cluster_clients.items():
        operators[cluster] = install_operator_on_cluster(cluster, api_client, settings, base_helm_args)
    return operators


def main(argv: Optional[List[str]] = None) -> None:
    parser = argparse.ArgumentParser(description="Decentralized multi-cluster POC installer")
    parser.add_argument("--dry-run", action="store_true", help="render all objects as YAML, apply nothing")
    parser.add_argument("--out", default=".decentralized-dry-run", help="dry-run output directory")
    args = parser.parse_args(argv)

    if not args.dry_run:
        parser.error("the live path runs through the smoke test (gated); this entry point only supports --dry-run")

    settings = settings_from_env()
    inventory = render_dry_run(settings, args.out)
    for cluster, counts in inventory.items():
        rendered = ", ".join(f"{kind} x{count}" for kind, count in sorted(counts.items()))
        print(f"{cluster}: {rendered}")
    print(f"Rendered to {os.path.abspath(args.out)} (helm values and chart CRD list per cluster alongside)")


if __name__ == "__main__":
    main()

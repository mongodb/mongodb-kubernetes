"""Unit tests over the installer's dry-run rendering: the B5 receipt. No cluster, no OM."""

import yaml

from tests.multicluster_decentralized.installer import InstallerSettings, plan_cluster_objects, render_dry_run

CLUSTERS = ["kind-e2e-cluster-1", "kind-e2e-cluster-2", "kind-e2e-cluster-3"]


def make_settings() -> InstallerSettings:
    return InstallerSettings(
        clusters=CLUSTERS,
        namespace="mdb-ns",
        om_base_url="http://om:8080",
        om_org_id="org1",
        om_user="jane.doe@example.com",
        om_api_key="api-key",
        project_name="mdb-ns",
    )


def load_rendered(cluster_dir, kind_fragment):
    docs = []
    for path in sorted(cluster_dir.iterdir()):
        if kind_fragment in path.name and path.suffix == ".yaml":
            with open(path) as f:
                docs.append(yaml.safe_load(f))
    return docs


def test_dry_run_renders_the_per_cluster_inventory(tmp_path):
    inventory = render_dry_run(make_settings(), str(tmp_path))

    assert sorted(inventory) == sorted(CLUSTERS)
    for cluster in CLUSTERS:
        # Secrets: own peer token, OM credentials, agent API key, and one kubeconfig credential
        # per peer.
        assert inventory[cluster] == {
            "Namespace": 1,
            "ServiceAccount": 1,
            "Secret": 5,
            "Role": 1,
            "RoleBinding": 1,
            "ConfigMap": 1,
            "MemberCluster": 2,
            "OperatorConfig": 1,
            "MongoDBMultiCluster": 1,
        }


def test_dry_run_helm_values_pin_identity_and_elect_by_default(tmp_path):
    render_dry_run(make_settings(), str(tmp_path))

    for cluster in CLUSTERS:
        with open(tmp_path / cluster / "helm-values.yaml") as f:
            values = yaml.safe_load(f)
        assert values["operator.name"] == "mongodb-kubernetes-operator-decentralized"
        assert values["operator.clusterIdentity.clusterName"] == cluster
        # No forced leader: the quorum lease election decides who leads.
        assert "OPERATOR_LEADER_CLUSTER_NAME" not in values.get("customEnvVars", "")


def test_dry_run_helm_values_can_force_a_leader_for_debugging(tmp_path):
    settings = make_settings()
    settings.forced_leader_cluster = "kind-e2e-cluster-2"
    render_dry_run(settings, str(tmp_path))

    for cluster in CLUSTERS:
        with open(tmp_path / cluster / "helm-values.yaml") as f:
            values = yaml.safe_load(f)
        assert "OPERATOR_LEADER_CLUSTER_NAME=kind-e2e-cluster-2" in values["customEnvVars"]


def test_dry_run_registers_exactly_the_two_peers(tmp_path):
    render_dry_run(make_settings(), str(tmp_path))

    members = load_rendered(tmp_path / "kind-e2e-cluster-2", "membercluster")
    assert sorted(m["spec"]["clusterName"] for m in members) == ["kind-e2e-cluster-1", "kind-e2e-cluster-3"]
    for member in members:
        assert member["spec"]["credentialSecretRef"]["name"] == f"mck-credential-{member['spec']['clusterName']}"

    credentials = load_rendered(tmp_path / "kind-e2e-cluster-2", "secret-mck-credential")
    for credential in credentials:
        kubeconfig = yaml.safe_load(credential["stringData"]["kubeconfig"])
        peer = kubeconfig["current-context"]
        assert peer in ("kind-e2e-cluster-1", "kind-e2e-cluster-3")
        assert kubeconfig["clusters"][0]["cluster"]["server"] == f"https://placeholder-cluster-ip.{peer}"


def test_the_workload_cr_and_om_inputs_are_identical_everywhere(tmp_path):
    render_dry_run(make_settings(), str(tmp_path))

    crs = [load_rendered(tmp_path / c, "mongodbmulticluster")[0] for c in CLUSTERS]
    assert crs[0] == crs[1] == crs[2]
    assert [e["clusterName"] for e in crs[0]["spec"]["clusterSpecList"]] == CLUSTERS

    config_maps = [load_rendered(tmp_path / c, "configmap")[0] for c in CLUSTERS]
    assert config_maps[0] == config_maps[1] == config_maps[2]

    operator_configs = [load_rendered(tmp_path / c, "operatorconfig")[0] for c in CLUSTERS]
    for operator_config in operator_configs:
        # mongodbdirectives is opt-in only; without it the member controllers are deaf.
        assert operator_config["spec"]["watchedResources"] == ["mongodbdirectives"]


def test_om_ca_ships_on_every_cluster_when_configured(tmp_path):
    """With a private-CA OM, the OM CA ConfigMap must exist on every cluster (the pods mount it
    by name and nothing else creates it in decentralized mode) and the project ConfigMap must
    reference it."""
    settings = make_settings()
    settings.om_ca_pem = "OM CA PEM"
    render_dry_run(settings, str(tmp_path))

    for cluster in CLUSTERS:
        config_maps = {cm["metadata"]["name"]: cm for cm in load_rendered(tmp_path / cluster, "configmap")}
        assert config_maps["om-ca"]["data"] == {"mms-ca.crt": "OM CA PEM"}
        assert config_maps["my-project"]["data"]["sslMMSCAConfigMap"] == "om-ca"


def test_include_workload_cr_can_be_turned_off():
    """The fixture-swap path (tests create the workload CR themselves) sets
    include_workload_cr=False; every other object stays identical, in order."""
    settings = make_settings()
    with_cr = plan_cluster_objects(CLUSTERS[0], settings)

    settings.include_workload_cr = False
    without_cr = plan_cluster_objects(CLUSTERS[0], settings)

    assert without_cr == with_cr[:-1]
    assert not any(o["kind"] == "MongoDBMultiCluster" for o in without_cr)


def test_dry_run_lists_the_chart_crds(tmp_path):
    render_dry_run(make_settings(), str(tmp_path))

    for cluster in CLUSTERS:
        with open(tmp_path / cluster / "crds-from-chart.txt") as f:
            crds = f.read().split()
        assert "operator.mongodb.com_mongodbdirectives.yaml" in crds
        assert "operator.mongodb.com_memberclusters.yaml" in crds

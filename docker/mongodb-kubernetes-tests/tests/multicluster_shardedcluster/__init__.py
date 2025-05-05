from typing import Dict, List

import kubernetes
import pytest
from kubernetes.client import ApiClient
from kubetester import get_statefulset
from kubetester.kubetester import KubernetesTester, is_multi_cluster
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MultiClusterClient
from tests import test_logger
from tests.shardedcluster.conftest import (
    get_member_cluster_clients_using_cluster_mapping,
)

logger = test_logger.get_test_logger(__name__)


# Expand shard overrides (they can contain multiple shard names) and build a mapping from shard name to
# its override configuration
def expand_shard_overrides(sc_spec) -> Dict:
    resource_shard_overrides = sc_spec.get("shardOverrides", [])
    resource_shard_override_map = {}
    for resource_override in resource_shard_overrides:
        for ac_shard_name in resource_override["shardNames"]:
            resource_shard_override_map[ac_shard_name] = resource_override
    return resource_shard_override_map


# Compare the applied resource to the automation config, and ensure shard, mongos, and config server counts are correct
def validate_member_count_in_ac(sharded_cluster: MongoDB, automation_config):
    resource_spec = sharded_cluster["spec"]

    if is_multi_cluster():
        shard_count = resource_spec["shardCount"]
        # Cfg serv and mongos count from cluster spec lists
        mongos_cluster_specs = resource_spec.get("mongos", {}).get("clusterSpecList", [])
        mongos_count = sum(spec.get("members", 1) for spec in mongos_cluster_specs)  # Default to 1 if no member
        config_cluster_specs = resource_spec.get("configSrv", {}).get("clusterSpecList", [])
        config_server_count = sum(spec.get("members", 1) for spec in config_cluster_specs)
    else:
        shard_count = resource_spec["shardCount"]
        mongos_count = resource_spec["mongosCount"]
        config_server_count = resource_spec["configServerCount"]

    automation_processes = automation_config["processes"]
    automation_replica_sets = automation_config["replicaSets"]
    automation_sharding = automation_config["sharding"][0]

    # Verify shard count
    automation_shards = automation_sharding["shards"]
    assert shard_count == len(
        automation_shards
    ), f"Shard count mismatch: expected {shard_count}, got {len(automation_shards)}"

    # Verify mongos count
    automation_mongos_processes = [p for p in automation_processes if p["processType"] == "mongos"]
    assert mongos_count == len(
        automation_mongos_processes
    ), f"Mongos count mismatch: expected {mongos_count}, got {len(automation_mongos_processes)}"

    # Verify config server count
    config_rs_list = [
        rs for rs in automation_replica_sets if rs["_id"] == f"{sharded_cluster['metadata']['name']}-config"
    ]
    assert len(config_rs_list) == 1, f"There must be exactly one config server replicaset, found {len(config_rs_list)}"
    config_members = config_rs_list[0]["members"]
    assert config_server_count == len(
        config_members
    ), f"Config server count mismatch: expected {config_server_count}, got {len(config_members)}"

    logger.info(f"Cluster {sharded_cluster.name} has the correct member counts")
    logger.debug(f"{shard_count} shards, {mongos_count} mongos, {config_server_count} configs")


def assert_member_configs(expected_member_configs: List[Dict[str, str]], ac_members: List[Dict], ac_shard_name: str):
    logger.debug(f"Ensuring member config correctness of shard {ac_shard_name}")
    if expected_member_configs:
        for idx, ac_member in enumerate(ac_members):
            expected_config = expected_member_configs[idx]
            expected_priority = int(expected_config.get("priority", "1"))
            expected_votes = int(expected_config.get("votes", 1))
            actual_priority = ac_member["priority"]
            actual_votes = ac_member["votes"]
            assert (
                expected_priority == actual_priority
            ), f"Shard {ac_shard_name} member {idx}: expected priority {expected_priority}, got {actual_priority}"
            assert (
                expected_votes == actual_votes
            ), f"Shard {ac_shard_name} member {idx}: expected votes {expected_votes}, got {actual_votes}"
    else:
        # If no member config, the default value for votes and priorities is 1
        for idx, ac_member in enumerate(ac_members):
            assert (
                ac_member["priority"] == 1
            ), f"Shard {ac_shard_name} member {idx}: expected default priority 1, got {ac_member['priority']}"
            assert (
                ac_member["votes"] == 1
            ), f"Shard {ac_shard_name} member {idx}: expected default votes 1, got {ac_member['votes']}"
    logger.info(f"Shard {ac_shard_name} has the correct values for votes and priorities")


# Compare the applied resource to the automation config, and ensure Members, votes and priorities are correct
def validate_shard_configurations_in_ac(sharded_cluster: MongoDB, automation_config):
    resource_spec = sharded_cluster["spec"]
    resource_mongods_per_shard = resource_spec["mongodsPerShardCount"]
    resource_shard_overrides = resource_spec.get("shardOverrides", [])
    ac_replica_sets = automation_config["replicaSets"]
    ac_sharding = automation_config["sharding"][0]
    ac_automation_shards = ac_sharding["shards"]
    # Build a mapping from shard name to its override configuration
    resource_shard_override_map = {}
    for resource_override in resource_shard_overrides:
        for ac_shard_name in resource_override["shardNames"]:
            resource_shard_override_map[ac_shard_name] = resource_override
    for ac_shard in ac_automation_shards:
        ac_shard_name = ac_shard["_id"]
        ac_replica_set_name = ac_shard["rs"]
        # Filter by name to get replicaset config
        rs_list = list(filter(lambda rs: rs["_id"] == ac_replica_set_name, ac_replica_sets))
        if rs_list:
            rs_config = rs_list[0]
        else:
            raise ValueError(f"Replica set {ac_replica_set_name} not found in automation config.")
        ac_members = rs_config["members"]
        resource_override = resource_shard_override_map.get(ac_shard_name)
        if resource_override:
            expected_member_configs = resource_override.get("memberConfig", [])
            expected_members = resource_override.get("members", len(expected_member_configs))
        else:
            expected_members = resource_mongods_per_shard
            expected_member_configs = None  # By default, there is no member config
        assert expected_members == len(
            ac_members
        ), f"Shard {ac_shard_name}: expected {expected_members} members, got {len(ac_members)}"
        # Verify member configurations
        assert_member_configs(expected_member_configs, ac_members, ac_shard_name)


# Compare the applied resource to the automation config, and ensure Members, votes and priorities are correct
def validate_shard_configurations_in_ac_multi(sharded_cluster: MongoDB, automation_config):
    resource_spec = sharded_cluster["spec"]
    ac_replica_sets = automation_config["replicaSets"]
    ac_sharding = automation_config["sharding"][0]

    ac_automation_shards = ac_sharding["shards"]
    resource_shard_override_map = expand_shard_overrides(resource_spec)

    # Build default member configurations
    default_shard_spec = resource_spec.get("shard", {})
    default_cluster_spec_list = default_shard_spec.get("clusterSpecList", [])
    default_member_configs = []
    for cluster_spec in default_cluster_spec_list:
        members = cluster_spec.get("members", 1)
        member_configs = cluster_spec.get("memberConfig", [{"priority": "1", "votes": 1}] * members)
        default_member_configs.extend(member_configs)

    for ac_shard in ac_automation_shards:
        ac_shard_name = ac_shard["_id"]
        ac_replica_set_name = ac_shard["rs"]

        # Get replicaset config from automation config
        rs_list = list(filter(lambda rs: rs["_id"] == ac_replica_set_name, ac_replica_sets))
        if rs_list:
            rs_config = rs_list[0]
        else:
            raise ValueError(f"Replica set {ac_replica_set_name} not found in automation config.")

        ac_members = rs_config["members"]
        resource_override = resource_shard_override_map.get(ac_shard_name)

        # Build expected member configs
        if resource_override:
            cluster_spec_list = resource_override.get("clusterSpecList", [])
            expected_member_configs = []
            for cluster_spec in cluster_spec_list:
                members = cluster_spec.get("members", 1)
                member_configs = cluster_spec.get("memberConfig", [{}] * members)
                expected_member_configs.extend(member_configs)
        else:
            expected_member_configs = default_member_configs

        logger.debug(f"Testing {ac_shard_name} member count, expecting {len(expected_member_configs)} members")
        assert len(ac_members) == len(
            expected_member_configs
        ), f"Shard {ac_shard_name}: expected {len(expected_member_configs)} members, got {len(ac_members)}"
        logger.info(f"Shard {ac_shard_name} has the correct number of members: {len(expected_member_configs)}")

        # Verify member configurations
        assert_member_configs(expected_member_configs, ac_members, ac_shard_name)


def build_expected_statefulsets(sc) -> Dict[str, int]:
    sc_name = sc.name
    sc_spec = sc["spec"]
    shard_count = sc_spec["shardCount"]
    mongods_per_shard = sc_spec["mongodsPerShardCount"]
    shard_override_map = expand_shard_overrides(sc_spec)

    # Dict holding expected sts names and expected replica counts
    expected_statefulsets = {}

    # Shards sts
    for i in range(shard_count):
        shard_sts_name = f"{sc_name}-{i}"
        override = shard_override_map.get(shard_sts_name)
        members = mongods_per_shard
        if override and "members" in override:
            # If 'members' is not specified in the override, we keep the default 'mongodsPerShardCount'
            members = override.get("members")
        expected_statefulsets[shard_sts_name] = members

    # Config server and mongos sts
    expected_statefulsets[f"{sc_name}-config"] = sc_spec["configServerCount"]
    expected_statefulsets[f"{sc_name}-mongos"] = sc_spec["mongosCount"]

    return expected_statefulsets


def build_expected_statefulsets_multi(sc: MongoDB, cluster_mapping: Dict[str, int]) -> Dict[str, Dict[str, int]]:
    sc_name = sc.name
    sc_spec = sc["spec"]
    shard_count = sc_spec["shardCount"]
    shard_override_map = expand_shard_overrides(sc_spec)

    # Dict holding expected sts per cluster: {cluster_name: {sts_name: replica_count}}
    expected_clusters_sts = {}

    # Process each shard
    for i in range(shard_count):
        shard_name = f"{sc_name}-{i}"
        override = shard_override_map.get(shard_name)
        if override:
            shard_cluster_spec_list = override.get("clusterSpecList", [])
        else:
            default_shard_spec = sc_spec.get("shard", {})
            shard_cluster_spec_list = default_shard_spec.get("clusterSpecList", [])

        update_expected_sts(shard_name, shard_cluster_spec_list, expected_clusters_sts, cluster_mapping)

    # Process config servers and mongos
    config_spec = sc_spec.get("configSrv", {})
    config_cluster_spec_list = config_spec.get("clusterSpecList", [])
    update_expected_sts(f"{sc_name}-config", config_cluster_spec_list, expected_clusters_sts, cluster_mapping)

    mongos_spec = sc_spec.get("mongos", {})
    mongos_cluster_spec_list = mongos_spec.get("clusterSpecList", [])
    update_expected_sts(f"{sc_name}-mongos", mongos_cluster_spec_list, expected_clusters_sts, cluster_mapping)

    logger.debug(f"Expected statefulsets: {expected_clusters_sts}")
    return expected_clusters_sts


def update_expected_sts(sts_prefix: str, clusterspeclist, expected_clusters_sts, cluster_mapping: Dict[str, int]):
    for idx, cluster_spec in enumerate(clusterspeclist):
        cluster_name = cluster_spec["clusterName"]
        # The name of the sts is based on the unique cluster index, stored in the state configmap
        cluster_idx_in_state = cluster_mapping.get(cluster_name)
        if cluster_idx_in_state is None:
            raise AssertionError(f"Cluster {cluster_name} ist not in the state, cluster mapping is {cluster_mapping}")
        members = cluster_spec.get("members", 1)
        sts_name = f"{sts_prefix}-{cluster_idx_in_state}"
        if cluster_name not in expected_clusters_sts:
            expected_clusters_sts[cluster_name] = {}
        expected_clusters_sts[cluster_name][sts_name] = members


# Fetch each expected statefulset from the cluster and assert correct replica count
def validate_correct_sts_in_cluster(
    expected_statefulsets: Dict[str, int], namespace: str, cluster_name: str, client: ApiClient
):
    for sts_name, expected_replicas in expected_statefulsets.items():
        try:
            sts = get_statefulset(namespace, sts_name, client)
        except kubernetes.client.exceptions.ApiException as e:
            if e.status == 404:
                raise AssertionError(
                    f"StatefulSet {sts_name} not found in cluster {cluster_name} namespace {namespace}."
                )
            else:
                raise

        actual_replicas = sts.spec.replicas
        assert (
            actual_replicas == expected_replicas
        ), f"StatefulSet {sts_name} in cluster {cluster_name}: expected {expected_replicas} replicas, got {actual_replicas}"
        logger.info(
            f"StatefulSet {sts_name} in cluster {cluster_name} has the correct number of replicas: {actual_replicas}"
        )


def validate_correct_sts_in_cluster_multi(
    expected_statefulsets_per_cluster: Dict[str, Dict[str, int]],
    namespace: str,
    member_cluster_clients: list[MultiClusterClient],
):
    for cluster_name, sts_dict in expected_statefulsets_per_cluster.items():
        # Retrieve client from the list
        client = None
        for member_cluster_client in member_cluster_clients:
            if member_cluster_client.cluster_name == cluster_name:
                client = member_cluster_client
        if not client:
            raise AssertionError(f"ApiClient for cluster {cluster_name} not found.")

        validate_correct_sts_in_cluster(sts_dict, namespace, cluster_name, client.api_client)


def assert_correct_automation_config_after_scaling(sc: MongoDB):
    config = KubernetesTester.get_automation_config()
    validate_member_count_in_ac(sc, config)
    validate_shard_configurations_in_ac_multi(sc, config)


def assert_shard_sts_members_count(sc: MongoDB, shard_in_cluster_distribution: List[List[int]]):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        for shard_idx, shard_distribution in enumerate(shard_in_cluster_distribution):
            sts_name = sc.shard_statefulset_name(shard_idx, cluster_member_client.cluster_index)
            expected_shard_members_in_cluster = shard_distribution[cluster_member_client.cluster_index]
            cluster_member_client.assert_sts_members_count(sts_name, sc.namespace, expected_shard_members_in_cluster)


def assert_config_srv_sts_members_count(sc: MongoDB, config_srv_distribution: List[int]):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        sts_name = sc.config_srv_statefulset_name(cluster_member_client.cluster_index)
        expected_config_srv_members_in_cluster = config_srv_distribution[cluster_member_client.cluster_index]
        cluster_member_client.assert_sts_members_count(sts_name, sc.namespace, expected_config_srv_members_in_cluster)


def assert_mongos_sts_members_count(sc: MongoDB, mongos_distribution: List[int]):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        sts_name = sc.mongos_statefulset_name(cluster_member_client.cluster_index)
        expected_mongos_members_in_cluster = mongos_distribution[cluster_member_client.cluster_index]
        cluster_member_client.assert_sts_members_count(sts_name, sc.namespace, expected_mongos_members_in_cluster)

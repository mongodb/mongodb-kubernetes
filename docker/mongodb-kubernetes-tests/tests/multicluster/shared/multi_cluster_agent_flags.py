from typing import List

from kubetester import client
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.common.placeholders import placeholders


def test_create_mongodb_multi(multi_cluster_operator: Operator, mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


def test_multi_replicaset_has_agent_flags(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    cluster_1_client = member_cluster_clients[0]
    cmd = [
        "/bin/sh",
        "-c",
        "ls /var/log/mongodb-mms-automation/customLogFile* | wc -l",
    ]
    result = KubernetesTester.run_command_in_pod_container(
        "multi-replica-set-0-0",
        namespace,
        cmd,
        container="mongodb-enterprise-database",
        api_client=cluster_1_client.api_client,
    )
    assert result != "0"


def test_placeholders_in_external_services(
    namespace: str,
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    for cluster_spec_item in mongodb_multi["spec"]["clusterSpecList"]:
        annotations = placeholders.get_annotations_with_placeholders_for_multi_cluster(
            prefix=f'{cluster_spec_item["clusterName"]},'
        )
        external_access = cluster_spec_item.get("externalAccess", {})
        external_service = external_access.get("externalService", {})
        external_service["annotations"] = annotations
        external_access["externalService"] = external_service
        cluster_spec_item["externalAccess"] = external_access

    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=300)

    name = mongodb_multi["metadata"]["name"]
    for _, member_cluster_client in enumerate(member_cluster_clients):
        members = mongodb_multi.get_item_spec(member_cluster_client.cluster_name)["members"]
        for pod_idx in range(0, members):
            cluster_idx = member_cluster_client.cluster_index
            service = client.CoreV1Api(api_client=member_cluster_client.api_client).read_namespaced_service(
                f"{name}-{cluster_idx}-{pod_idx}-svc-external", namespace
            )
            cluster_name = member_cluster_client.cluster_name
            expected_annotations = placeholders.get_expected_annotations_multi_cluster(
                name=name,
                namespace=namespace,
                pod_idx=pod_idx,
                cluster_index=cluster_idx,
                cluster_name=cluster_name,
                prefix=f"{cluster_name},",
            )
            assert service.metadata.annotations == expected_annotations

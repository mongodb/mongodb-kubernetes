#!/usr/bin/env python3

import sys
from typing import List

import boto3


def get_instance_groups_for_cluster(cluster_name: str) -> List[str]:
    """
    :param cluster_name: name of the cluster
    :return: list of instance groups associated with that cluster
    """
    return [f"nodes.{cluster_name}", f"masters.{cluster_name}"]


def get_other_instance_groups_for_cluster(cluster_name: str, all_clusters: List[str]) -> List[str]:
    """
    :param cluster_name: the name of the cluster
    :param all_clusters: a list of all clusters
    :return: a list of instance group names that need to have rules added for
    """
    other_instance_groups = []
    for cluster in all_clusters:
        if cluster == cluster_name:
            continue
        other_instance_groups.extend(get_instance_groups_for_cluster(cluster))
    return other_instance_groups


def get_all_instance_groups(cluster_names: List[str]) -> List[str]:
    """
    :param cluster_names: list of all cluster names.
    :return: list of all instance group names.
    """
    all_instance_groups = []
    for cluster in cluster_names:
        all_instance_groups.extend(get_instance_groups_for_cluster(cluster))
    return all_instance_groups


def get_security_group_by_name(security_groups, name: str):
    """
    :param security_groups: list of all security group objects.
    :param name: name of the desired security group.
    :return:
    """
    return next(iter([sg for sg in security_groups if sg.group_name == name]))


def _add_all_traffic_security_group_rule(sg0, sg1, vpc_id):
    """
    :param sg0: security group that the ingres will be added to.
    :param sg1: security group that the inbound rule will be added to.
    :param vpc_id: vpc id of both security groups.
    """
    sg0.authorize_ingress(
        IpPermissions=[
            {
                "IpRanges": [{"CidrIp": "0.0.0.0/0"}],
                "IpProtocol": "-1",
                "Ipv6Ranges": [],
                "PrefixListIds": [],
                "UserIdGroupPairs": [{"VpcId": vpc_id, "GroupId": sg1.id, "UserId": "268558157000"}],
            },
        ]
    )


def main(
    region: str,
    vpc_id: str,
    cluster_names: List[str],
):
    """
    This script creates inbound rules allowing all traffic between all instances groups
    associated with all of the clusters provided.


    :param region: the aws region.
    :param vpc_id: the vpc id associated with the clusters.
    :param cluster_names: the names of the clusters.
    :return: None
    """
    ec2 = boto3.resource("ec2", region_name=region)

    security_groups = ec2.security_groups.all()
    for cluster in cluster_names:
        cluster_instance_groups = get_instance_groups_for_cluster(cluster)
        other_instance_group_names = get_other_instance_groups_for_cluster(cluster, cluster_names)

        for instance_group in cluster_instance_groups:
            instance_group_sg = get_security_group_by_name(security_groups, instance_group)
            for other_instance_group in other_instance_group_names:
                other_instance_group_sg = get_security_group_by_name(security_groups, other_instance_group)
                print(f"adding rule for {instance_group_sg.group_name} to {other_instance_group_sg.group_name}")
                try:
                    _add_all_traffic_security_group_rule(instance_group_sg, other_instance_group_sg, vpc_id)
                except Exception as e:
                    print(e)


if __name__ == "__main__":
    if len(sys.argv) != 4:
        raise ValueError("Usage: create_security_groups.py <region> <vpc_id> <clusters>")

    region = sys.argv[1]
    vpc_id = sys.argv[2]
    cluster_names = sys.argv[3].split(",")

    main(region, vpc_id, cluster_names)

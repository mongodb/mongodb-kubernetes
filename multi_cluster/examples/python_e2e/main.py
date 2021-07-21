#!/usr/bin/env python

from kubernetes import client, config


def main():
    """
    This is an example of how to access multiple clusters using a kube config file and service account tokens.
    """
    # update these tokens with the contents of the service account secret token from each cluster.
    cluster_1_token = "<token1>"
    cluster_2_token = "<token2>"
    _list_pods_in_cluster("e2e.cluster1.mongokubernetes.com", cluster_1_token)
    _list_pods_in_cluster("e2e.cluster2.mongokubernetes.com", cluster_2_token)


def _list_pods_in_cluster(cluster_name: str, token: str):
    config.load_kube_config(context=cluster_name)
    configuration = client.Configuration()
    configuration.host = f"https://api.{cluster_name}"
    configuration.verify_ssl = False
    configuration.api_key = {"authorization": f"Bearer {token}"}

    api_client = client.ApiClient(configuration=configuration)
    v1 = client.CoreV1Api(api_client=api_client)
    print(f"Listing pods with their IPs in: {cluster_name}")
    ret = v1.list_pod_for_all_namespaces(watch=False)
    for i in ret.items:
        print("%s\t%s\t%s" %
              (i.status.pod_ip, i.metadata.namespace, i.metadata.name))


if __name__ == '__main__':
    main()

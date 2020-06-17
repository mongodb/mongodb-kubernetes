import json
from typing import Dict

import yaml
from kubernetes import client
from kubernetes.client.rest import ApiException


def pytest_runtest_setup(item):
    """ This removes the default operator fixture for all the tests in the current directory """
    if "default_operator" in item.fixturenames:
        item.fixturenames.remove("default_operator")


def pytest_runtest_logreport(report):
    """ This allows to dump some reporting information on failure as usual dump information
    (see 'dump_diagnostic_information') prints only the status of objects in one single namespace.
    This information includes any MongoDB and MongoDBOpsManager objects in any namespace. """
    if report.outcome == "failed":
        print()
        print(
            "################################################################################"
        )
        print(
            "=> The test has failed, printing the diagnostic information about MongoDB and MongoDBOpsManager resources"
        )
        for ns in client.CoreV1Api().list_namespace().items:
            print(f"---> Namespace: {ns.metadata.name}")

            print_mdbs(ns.metadata.name)
            print_ops_managers(ns.metadata.name)


def print_statefulsets(ns: str):
    for sts in client.AppsV1Api().list_namespaced_stateful_set(ns).items:
        print_header(f"StatefulSet: {sts.metadata.name}")
        print(sts)


def print_pods(ns: str):
    for pod in client.CoreV1Api().list_namespaced_pod(ns).items:
        print_header(f"Pod: {pod.metadata.name}")
        print(pod)


def print_mdbs(ns: str):
    try:
        # TODO if no resources exist the api prints the "(404) Reason: Not Found... " message (but doesn't fail) - is
        # there a way to avoid it?
        for custom_object in client.CustomObjectsApi().list_namespaced_custom_object(
            "mongodb.com", "v1", ns, "mongodbs", pretty=True
        )["items"]:
            print_header("MongoDB: {}".format(custom_object["metadata"]["name"]))
            print(json.dumps(custom_object, indent=4))

            # note, that we print statefulsets and pods only if there are Custom Resources
            print_statefulsets(ns)
            print_pods(ns)
    except ApiException as e:
        print(e)


def print_ops_managers(ns: str):
    try:
        for custom_object in client.CustomObjectsApi().list_namespaced_custom_object(
            "mongodb.com", "v1", ns, "opsmanagers", pretty=True
        )["items"]:
            print_header(
                "MongoDBOpsManager: {}".format(custom_object["metadata"]["name"])
            )
            print(json.dumps(custom_object, indent=4))

            print_statefulsets(ns)
            print_pods(ns)
    except ApiException as e:
        print(e)


def print_header(msg: str):
    print("------------------------------------------------------------")
    print(msg)
    print("------------------------------------------------------------")

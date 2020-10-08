from __future__ import annotations

from typing import Dict, List, Optional

import time
import logging

from kubernetes import client
from kubernetes.client import V1Pod, V1beta1CustomResourceDefinition, V1Deployment
from kubernetes.client.rest import ApiException
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.helm import helm_install, helm_upgrade, helm_template, helm_uninstall

OPERATOR_CRDS = (
    "mongodb.mongodb.com",
    "mongodbusers.mongodb.com",
    "opsmanagers.mongodb.com",
)


class Operator(object):
    """Operator is an abstraction over some Operator and relevant  resources. It allows to create and delete
    the Operator deployment and K8s resources. """

    def __init__(
        self,
        namespace: str,
        helm_args: Optional[Dict] = None,
        helm_options: Optional[List[str]] = None,
        helm_chart_path: Optional[str] = "helm_chart",
        name: Optional[str] = "mongodb-enterprise-operator",
    ):
        if helm_args is None:
            helm_args = {}

        helm_args["namespace"] = namespace
        helm_args["operator.env"] = "dev"

        self.namespace = namespace
        self.helm_arguments = helm_args
        self.helm_options = helm_options
        self.helm_chart_path = helm_chart_path
        self.name = name

    def install_from_template(self):
        """ Uses helm to generate yaml specification and then uses python K8s client to apply them to the cluster
        This is equal to helm template...| kubectl apply - """
        yaml_file = helm_template(
            self.helm_arguments, helm_chart_path=self.helm_chart_path
        )
        create_or_replace_from_yaml(client.api_client.ApiClient(), yaml_file)
        self._wait_for_operator_ready()

        return self

    def install(self) -> Operator:
        """ Installs the Operator to Kubernetes cluster using 'helm install', waits until it's running """
        helm_install(
            "mongodb-enterprise-operator",
            self.helm_arguments,
            helm_chart_path=self.helm_chart_path,
            helm_options=self.helm_options,
        )
        self._wait_for_operator_ready()

        return self

    def upgrade(self, install: bool = True) -> Operator:
        """ Upgrades the Operator in Kubernetes cluster using 'helm upgrade', waits until it's running """
        helm_upgrade(
            self.name,
            self.helm_arguments,
            install,
            helm_chart_path=self.helm_chart_path,
            helm_options=self.helm_options,
        )
        self._wait_for_operator_ready()

        return self

    def uninstall(self):
        helm_uninstall(self.name)

    def delete_operator_deployment(self):
        """ Deletes the Operator deployment from K8s cluster. """
        client.AppsV1Api().delete_namespaced_deployment(self.name, self.namespace)

    def list_operator_pods(self) -> List[V1Pod]:
        pods = (
            client.CoreV1Api()
            .list_namespaced_pod(
                self.namespace,
                label_selector="app.kubernetes.io/name={}".format(self.name),
            )
            .items
        )
        return pods

    def read_deployment(self) -> V1Deployment:
        return client.AppsV1Api().read_namespaced_deployment(self.name, self.namespace)

    def assert_is_running(self):
        """ Makes 3 checks that the Operator is running with 1 second interval. One check is not enough as the Operator may get
        to Running state for short and fail later """
        for _ in range(0, 3):
            pods = self.list_operator_pods()
            assert len(pods) == 1
            assert pods[0].status.phase == "Running"
            assert pods[0].status.container_statuses[0].ready
            time.sleep(1)

    def _wait_for_operator_ready(self, retries: int = 60):
        """ waits until the Operator deployment is ready. """
        # we need to give some time for the new pod to start instead of the existing one (if any)
        time.sleep(4)
        retry_count = retries
        while retry_count > 0:
            pods = self.list_operator_pods()
            if len(pods) == 1:
                if (
                    pods[0].status.phase == "Running"
                    and pods[0].status.container_statuses[0].ready
                ):
                    return
                if pods[0].status.phase == "Failed":
                    raise Exception(
                        "Operator failed to start: {}".format(pods[0].status.phase)
                    )
            time.sleep(1)
            retry_count = retry_count - 1

        # Operator hasn't started - printing some debug information
        self.print_diagnostics()

        raise Exception(
            f"Operator hasn't started in specified time after {retries} retries."
        )

    def print_diagnostics(self):
        logging.info("Operator Deployment: ")
        logging.info(self.read_deployment())

        pods = self.list_operator_pods()
        if len(pods) > 0:
            logging.info("Operator pods: %d", len(pods))
            logging.info("Operator spec: %s", pods[0].spec)
            logging.info("Operator status: %s", pods[0].status)


def delete_operator_crds():
    for crd_name in OPERATOR_CRDS:
        try:
            client.ApiextensionsV1beta1Api().delete_custom_resource_definition(crd_name)
        except ApiException as e:
            if e.status != 404:
                raise e


def list_operator_crds() -> List[V1beta1CustomResourceDefinition]:
    return sorted(
        [
            crd
            for crd in client.ApiextensionsV1beta1Api()
            .list_custom_resource_definition()
            .items
            if crd.metadata.name in OPERATOR_CRDS
        ],
        key=lambda crd: crd.metadata.name,
    )

import json
import os
import random
import re
import ssl
import string
import sys
import tarfile
import tempfile
import time
import warnings
from base64 import b64decode, b64encode
from typing import Dict, List, Optional

import jsonpatch
import kubernetes.client
import pymongo
import pytest
import requests
import semver
import yaml
from cryptography import x509
from cryptography.hazmat.backends import default_backend
from kubeobject import CustomObject
from kubernetes import client, config
from kubernetes.client.rest import ApiException
from kubernetes.stream import stream
from kubetester.crypto import wait_for_certs_to_be_issued
from opentelemetry import trace
from requests.auth import HTTPBasicAuth, HTTPDigestAuth
from tests import test_logger

TRACER = trace.get_tracer("evergreen-agent")
logger = test_logger.get_test_logger(__name__)

SSL_CA_CERT = "/var/run/secrets/kubernetes.io/serviceaccount/..data/ca.crt"
EXTERNALLY_MANAGED_TAG = "EXTERNALLY_MANAGED_BY_KUBERNETES"
MAX_TAG_LEN = 32

DEPRECATION_WARNING = "This feature has been DEPRECATED and should only be used in testing environments."
AGENT_WARNING = "The Operator is generating TLS x509 certificates for agent authentication. " + DEPRECATION_WARNING
MEMBER_AUTH_WARNING = (
    "The Operator is generating TLS x509 certificates for internal cluster authentication. " + DEPRECATION_WARNING
)
SERVER_WARNING = "The Operator is generating TLS certificates for server authentication. " + DEPRECATION_WARNING

plural_map = {
    "MongoDB": "mongodb",
    "MongoDBUser": "mongodbusers",
    "MongoDBOpsManager": "opsmanagers",
    "MongoDBMultiCluster": "mongodbmulticluster",
}

from opentelemetry import trace

TRACER = trace.get_tracer("evergreen-agent")
logger = test_logger.get_test_logger(__name__)


def running_locally():
    return os.getenv("POD_NAME", "local") == "local"


def is_multi_cluster():
    return len(os.getenv("MEMBER_CLUSTERS", "")) > 0


def is_default_architecture_static() -> bool:
    return os.getenv("MDB_DEFAULT_ARCHITECTURE", "non-static") == "static"


def assert_container_count_with_static(current_container_count: int, expected_counter_without_static: int):
    if is_default_architecture_static():
        assert current_container_count == expected_counter_without_static + 1
    else:
        assert current_container_count == expected_counter_without_static


def get_default_architecture() -> str:
    return "static" if is_default_architecture_static() else "non-static"


def assert_statefulset_architecture(statefulset: client.V1StatefulSet, architecture: str):
    """
    Asserts that the statefulset is configured with the expected architecture.
    """
    agent_container = next((c for c in statefulset.spec.template.spec.containers if c.name == "mongodb-agent"), None)
    if architecture == "non-static":
        # In non-static architecture expect agent container to not be present
        assert agent_container is None
    else:
        # In static architecture we expect agent container to be present
        # and contain static environment variable which
        # instructs the agent launcher script to not download binaries
        assert agent_container is not None
        static_env_var = next(
            (env for env in agent_container.env if env.name == "MDB_STATIC_CONTAINERS_ARCHITECTURE"), None
        )
        assert static_env_var.value == "true"


skip_if_static_containers = pytest.mark.skipif(
    is_default_architecture_static(),
    reason="Skip if this test is executed using the Static Containers architecture",
)

skip_if_local = pytest.mark.skipif(running_locally(), reason="Only run in Kubernetes cluster")
skip_if_multi_cluster = pytest.mark.skipif(is_multi_cluster(), reason="Only run in Kubernetes single cluster")
# time to sleep between retries
SLEEP_TIME = 2
# no timeout (loop forever)
INFINITY = -1


class KubernetesTester(object):
    """
    KubernetesTester is the base class for all python tests. It deliberately doesn't have object state
    as it is not expected to have more than one concurrent instance running. All tests must be run in separate
    Kubernetes namespaces and use separate Ops Manager groups.
    The class provides some common utility methods used by all children and also performs some common
    create/update/delete actions for Kubernetes objects based on the docstrings of subclasses
    """

    init = None
    group_id = None

    @classmethod
    def setup_env(cls):
        """Optionally override this in a test instance to create an appropriate test environment."""
        pass

    @classmethod
    def teardown_env(cls):
        """Optionally override this in a test instance to destroy the test environment."""
        pass

    @classmethod
    def create_config_map(cls, namespace, name, data):
        """Create a config map in a given namespace with the given name and data."""
        config_map = cls.clients("client").V1ConfigMap(
            metadata=cls.clients("client").V1ObjectMeta(name=name), data=data
        )
        cls.clients("corev1").create_namespaced_config_map(namespace, config_map)

    @classmethod
    def patch_config_map(cls, namespace, name, data):
        """Patch a config map in a given namespace with the given name and data."""
        config_map = cls.clients("client").V1ConfigMap(data=data)
        cls.clients("corev1").patch_namespaced_config_map(name, namespace, config_map)

    @classmethod
    def create_secret(cls, namespace: str, name: str, data: Dict[str, str]):
        """
        Deprecated: use kubetester.create_secret instead.

        Create a secret in a given namespace with the given name and data—handles base64 encoding.
        """
        secret = cls.clients("client").V1Secret(
            metadata=cls.clients("client").V1ObjectMeta(name=name), string_data=data
        )

        try:
            cls.clients("corev1").create_namespaced_secret(namespace, secret)
        except client.rest.ApiException as e:
            if e.status == 409 and running_locally():
                pass

    @classmethod
    def update_secret(cls, namespace: str, name: str, data: Dict[str, str]):
        """
        Deprecated: use kubetester.update_secret instead.

        Updates a secret in a given namespace with the given name and data—handles base64 encoding.
        """
        secret = cls.clients("client").V1Secret(
            metadata=cls.clients("client").V1ObjectMeta(name=name), string_data=data
        )
        cls.clients("corev1").patch_namespaced_secret(name, namespace, secret)

    @classmethod
    def delete_secret(cls, namespace: str, name: str):
        """Delete a secret in a given namespace with the given name."""
        cls.clients("corev1").delete_namespaced_secret(name, namespace)

    @classmethod
    def delete_csr(cls, name: str):
        cls.clients("certificates").delete_certificate_signing_request(name)

    @classmethod
    def read_secret(cls, namespace: str, name: str) -> Dict[str, str]:
        """
        Deprecated: use kubetester.read_secret instead.
        """
        data = cls.clients("corev1").read_namespaced_secret(name, namespace).data
        return decode_secret(data=data)

    @classmethod
    def decode_secret(cls, data: Dict[str, str]) -> Dict[str, str]:
        return {k: b64decode(v).decode("utf-8") for (k, v) in data.items()}

    @classmethod
    def read_configmap(
        cls, namespace: str, name: str, api_client: Optional[client.ApiClient] = None, with_metadata=False
    ) -> Dict[str, str]:
        corev1 = cls.clients("corev1")
        if api_client is not None:
            corev1 = client.CoreV1Api(api_client=api_client)

        cm = corev1.read_namespaced_config_map(name, namespace)
        if with_metadata:
            return cm
        return cm.data

    @classmethod
    def read_pod(cls, namespace: str, name: str) -> Dict[str, str]:
        """Reads a Pod and returns its contents"""
        return cls.clients("corev1").read_namespaced_pod(name, namespace)

    @classmethod
    def read_pod_logs(
        cls,
        namespace: str,
        name: str,
        container: str = None,
        api_client: Optional[client.ApiClient] = None,
    ) -> str:
        return cls.clients("corev1", api_client=api_client).read_namespaced_pod_log(
            name=name, namespace=namespace, container=container
        )

    @classmethod
    def read_operator_pod(cls, namespace: str) -> Dict[str, str]:
        label_selector = "app.kubernetes.io/name=mongodb-enterprise-operator"
        return cls.read_pod_labels(namespace, label_selector).items[0]

    @classmethod
    def read_pod_labels(cls, namespace: str, label_selector: Optional[str] = None) -> Dict[str, str]:
        """Reads a Pod by labels."""
        return cls.clients("corev1").list_namespaced_pod(namespace=namespace, label_selector=label_selector)

    @classmethod
    def update_configmap(
        cls,
        namespace: str,
        name: str,
        data: Dict[str, str],
        api_client: Optional[client.ApiClient] = None,
    ):
        """Updates a ConfigMap in a given namespace with the given name and data—handles base64 encoding."""
        configmap = cls.clients("client", api_client=api_client).V1ConfigMap(
            metadata=cls.clients("client", api_client=api_client).V1ObjectMeta(name=name),
            data=data,
        )
        cls.clients("corev1").patch_namespaced_config_map(name, namespace, configmap)

    @classmethod
    def delete_configmap(cls, namespace: str, name: str, api_client: Optional[client.ApiClient] = None):
        """Delete a ConfigMap in a given namespace with the given name."""
        cls.clients("corev1", api_client=api_client).delete_namespaced_config_map(name, namespace)

    @classmethod
    def delete_service(cls, namespace: str, name: str):
        """Delete a Service in a given namespace with the given name."""
        cls.clients("corev1").delete_namespaced_service(name, namespace)

    @classmethod
    def create_namespace(cls, namespace_name):
        """Create a namespace with the given name."""
        namespace = cls.clients("client").V1Namespace(metadata=cls.clients("client").V1ObjectMeta(name=namespace_name))
        cls.clients("corev1").create_namespace(namespace)

    @classmethod
    def create_pod(cls, namespace: str, body: Dict):
        cls.clients("corev1").create_namespaced_pod(body=body, namespace=namespace)

    @classmethod
    def delete_pod(cls, namespace: str, name: str):
        """Delete a Pod in a given namespace with the given name."""
        cls.clients("corev1").delete_namespaced_pod(name, namespace)

    @classmethod
    def create_deployment(cls, namespace: str, body: Dict):
        cls.clients("appsv1").create_namespaced_deployment(body=body, namespace=namespace)

    @classmethod
    def create_service(
        cls,
        namespace: str,
        body: Dict,
        api_client: Optional[kubernetes.client.ApiClient] = None,
    ):
        cls.clients("corev1", api_client=api_client).create_namespaced_service(body=body, namespace=namespace)

    @classmethod
    def create_or_update_pvc(
        cls,
        namespace: str,
        body: Dict,
        storage_class_name: str = "gp2",
        api_client: Optional[kubernetes.client.ApiClient] = None,
    ):
        if storage_class_name is not None:
            body["spec"]["storageClassName"] = storage_class_name
        try:
            cls.clients("corev1", api_client=api_client).create_namespaced_persistent_volume_claim(
                body=body, namespace=namespace
            )
        except client.rest.ApiException as e:
            if e.status == 409:
                cls.clients("corev1", api_client=api_client).patch_namespaced_persistent_volume_claim(
                    body=body, name=body["metadata"]["name"], namespace=namespace
                )

    @classmethod
    def delete_pvc(cls, namespace: str, name: str):
        cls.clients("corev1").delete_namespaced_persistent_volume_claim(name, namespace=namespace)

    @classmethod
    def delete_namespace(cls, name):
        """Delete the specified namespace."""
        cls.clients("corev1").delete_namespace(name, body=cls.clients("client").V1DeleteOptions())

    @classmethod
    def delete_deployment(cls, namespace: str, name):
        cls.clients("appsv1").delete_namespaced_deployment(name, namespace)

    @staticmethod
    def clients(name, api_client: Optional[client.ApiClient] = None):
        return {
            "client": client,
            "corev1": client.CoreV1Api(api_client=api_client),
            "appsv1": client.AppsV1Api(api_client=api_client),
            "storagev1": client.StorageV1Api(api_client=api_client),
            "customv1": client.CustomObjectsApi(api_client=api_client),
            "certificates": client.CertificatesV1Api(api_client=api_client),
            "namespace": KubernetesTester.get_namespace(),
        }[name]

    @classmethod
    def teardown_class(cls):
        "Tears down testing class, make sure pytest ends after tests are run."
        cls.teardown_env()
        sys.stdout.flush()

    @classmethod
    def doc_string_to_init(cls, doc_string) -> dict:
        result = yaml.safe_load(doc_string)
        for m in ["create", "update"]:
            if m in result and "patch" in result[m]:
                result[m]["patch"] = json.loads(result[m]["patch"])
        return result

    @classmethod
    def setup_class(cls):
        "Will setup class (initialize kubernetes objects)"
        print("\n")
        KubernetesTester.load_configuration()
        # Loads the subclass doc
        if cls.init is None and cls.__doc__:
            cls.init = cls.doc_string_to_init(cls.__doc__)

        if cls.init:
            cls.prepare(cls.init, KubernetesTester.get_namespace())

        cls.setup_env()

    @staticmethod
    def load_configuration():
        "Loads kubernetes client configuration from kubectl config or incluster."
        try:
            config.load_kube_config()
        except Exception:
            config.load_incluster_config()

    @staticmethod
    def get_namespace():
        return get_env_var_or_fail("NAMESPACE")

    @staticmethod
    def get_om_group_name():
        return KubernetesTester.get_namespace()

    @staticmethod
    def get_om_base_url():
        return get_env_var_or_fail("OM_HOST")

    @staticmethod
    def get_om_user():
        return get_env_var_or_fail("OM_USER")

    @staticmethod
    def get_om_api_key():
        return get_env_var_or_fail("OM_API_KEY")

    @staticmethod
    def get_om_org_id():
        "Gets Organization ID. Makes sure to return None if it is not present"

        org_id = None
        # Do not fail if OM_ORGID is not set
        try:
            org_id = get_env_var_or_fail("OM_ORGID")
        except ValueError:
            pass

        if isinstance(org_id, str) and org_id.strip() == "":
            org_id = None

        return org_id

    @staticmethod
    def get_om_group_id(group_name=None, org_id=None):
        # doing some "caching" for the group id on the first invocation
        if (KubernetesTester.group_id is None) or group_name or org_id:
            group_name = group_name or KubernetesTester.get_om_group_name()

            org_id = org_id or KubernetesTester.get_om_org_id()

            group = KubernetesTester.query_group(group_name, org_id)

            KubernetesTester.group_id = group["id"]

            # Those are saved here so we can access om at the end of the test run and retrieve diagnostic data easily.
            if os.environ.get("OM_PROJECT_ID", ""):
                os.environ["OM_PROJECT_ID"] = os.environ["OM_PROJECT_ID"] + "," + KubernetesTester.group_id
            else:
                os.environ["OM_PROJECT_ID"] = KubernetesTester.group_id

        return KubernetesTester.group_id

    @classmethod
    def prepare(cls, test_setup, namespace):
        allowed_actions = ["create", "create_many", "update", "delete", "noop", "wait"]
        for action in [action for action in allowed_actions if action in test_setup]:
            rules = test_setup[action]

            if not isinstance(rules, list):
                rules = [rules]

            for rule in rules:
                KubernetesTester.execute(action, rule, namespace)

                if "wait_for_condition" in rule:
                    cls.wait_for_condition_string(rule["wait_for_condition"])
                elif "wait_for_message" in rule:
                    cls.wait_for_status_message(rule)
                else:
                    cls.wait_condition(rule)

    @staticmethod
    def execute(action, rules, namespace):
        "Execute function with name `action` with arguments `rules` and `namespace`"
        getattr(KubernetesTester, action)(rules, namespace)

    @staticmethod
    def wait_for(seconds):
        "Will wait for a given amount of seconds."
        time.sleep(int(seconds))

    @staticmethod
    def wait(rules, namespace):
        KubernetesTester.name = rules["resource"]
        KubernetesTester.wait_until(rules["until"], rules.get("timeout", 0))

    @staticmethod
    def create(section, namespace):
        "creates a custom object from filename"
        resource = yaml.safe_load(open(fixture(section["file"])))
        KubernetesTester.create_custom_resource_from_object(
            namespace,
            resource,
            exception_reason=section.get("exception", None),
            patch=section.get("patch", None),
        )

    @staticmethod
    def create_many(section, namespace):
        "creates multiple custom objects from a yaml list"
        resources = yaml.safe_load(open(fixture(section["file"])))
        for res in resources:
            name, kind = KubernetesTester.create_custom_resource_from_object(
                namespace,
                res,
                exception_reason=section.get("exception", None),
                patch=section.get("patch", None),
            )

    @staticmethod
    def create_mongodb_from_file(namespace, file_path):
        name, kind = KubernetesTester.create_custom_resource_from_file(namespace, file_path)
        KubernetesTester.namespace = namespace
        KubernetesTester.name = name
        KubernetesTester.kind = kind

    @staticmethod
    def create_custom_resource_from_file(namespace, file_path):
        with open(file_path) as f:
            resource = yaml.safe_load(f)
        return KubernetesTester.create_custom_resource_from_object(namespace, resource)

    @staticmethod
    def create_mongodb_from_object(namespace, resource, exception_reason=None, patch=None):
        name, kind = KubernetesTester.create_custom_resource_from_object(namespace, resource, exception_reason, patch)
        KubernetesTester.namespace = namespace
        KubernetesTester.name = name
        KubernetesTester.kind = kind

    @staticmethod
    def create_custom_resource_from_object(
        namespace,
        resource,
        exception_reason=None,
        patch=None,
        api_client: Optional[client.ApiClient] = None,
    ):
        name, kind, group, version, res_type = get_crd_meta(resource)
        if patch:
            patch = jsonpatch.JsonPatch(patch)
            resource = patch.apply(resource)

        KubernetesTester.namespace = namespace
        KubernetesTester.name = name
        KubernetesTester.kind = kind

        # For some long-running actions (e.g. creation of OpsManager) we may want to reuse already existing CR
        if os.getenv("SKIP_EXECUTION") == "true":
            print("Skipping creation as 'SKIP_EXECUTION' env variable is not empty")
            return

        print("Creating resource {} {} {}".format(kind, name, "(" + res_type + ")" if kind == "MongoDb" else ""))

        # TODO move "wait for exception" logic to a generic function and reuse for create/update/delete
        try:
            KubernetesTester.clients("customv1", api_client=api_client).create_namespaced_custom_object(
                group, version, namespace, plural(kind), resource
            )
        except ApiException as e:
            if e.status == 409:
                KubernetesTester.clients("customv1", api_client=api_client).patch_namespaced_custom_object(
                    group, version, namespace, plural(kind), name, resource
                )
            else:
                if isinstance(e.body, str):
                    # In Kubernetes v1.16+ the result body is a json string that needs to be parsed, according to
                    # whatever exception_reason was passed.
                    try:
                        body_json = json.loads(e.body)
                    except json.decoder.JSONDecodeError:
                        # The API did not return a JSON string
                        pass
                    else:
                        reason = validation_reason_from_exception(exception_reason)

                        if reason is not None:
                            field = exception_reason.split()[0]
                            for cause in body_json["details"]["causes"]:
                                if cause["reason"] == reason and cause["field"] == field:
                                    return None, None

                if exception_reason:
                    assert e.reason == exception_reason or exception_reason in e.body, "Real exception is: {}".format(e)
                    print(
                        '"{}" exception raised while creating the resource - this is expected!'.format(exception_reason)
                    )
                    return None, None

                print("Failed to create a resource ({}): \n {}".format(e, resource))
                raise

        else:
            if exception_reason:
                raise AssertionError("Expected ApiException, but create operation succeeded!")

        print("Created resource {} {} {}".format(kind, name, "(" + res_type + ")" if kind == "MongoDb" else ""))
        return name, kind

    @staticmethod
    def update(section, namespace):
        """
        Updates the resource in the "file" section, applying the jsonpatch in "patch" section.

        Python API client (patch_namespaced_custom_object) will send a "merge-patch+json" by default.
        This means that the resulting objects, after the patch is the union of the old and new objects. The
        patch can only change attributes or add, but not delete, as it is the case with "json-patch+json"
        requests. The json-patch+json requests are the ones used by `kubectl edit` and `kubectl patch`.

        # TODO:
        A fix for this has been merged already (https://github.com/kubernetes-client/python/issues/862). The
        Kubernetes Python module should be updated when the client is regenerated (version 10.0.1 or so)

        # TODO 2 (fixed in 10.0.1): As of 10.0.0 the patch gets completely broken: https://github.com/kubernetes-client/python/issues/866
        ("reason":"UnsupportedMediaType","code":415)
        So we still should be careful with "remove" operation - better use "replace: null"
        """
        resource = yaml.safe_load(open(fixture(section["file"])))

        patch = section.get("patch")
        KubernetesTester.patch_custom_resource_from_object(namespace, resource, patch)

    @staticmethod
    def patch_custom_resource_from_object(namespace, resource, patch):
        name, kind, group, version, res_type = get_crd_meta(resource)
        KubernetesTester.namespace = namespace
        KubernetesTester.name = name
        KubernetesTester.kind = kind

        if patch is not None:
            patch = jsonpatch.JsonPatch(patch)
            resource = patch.apply(resource)

        # For some long-running actions (e.g. update of OpsManager) we may want to reuse already existing CR
        if os.getenv("SKIP_EXECUTION") == "true":
            print("Skipping creation as 'SKIP_EXECUTION' env variable is not empty")
            return

        try:
            # TODO currently if the update doesn't pass (e.g. patch is incorrect) - we don't fail here...
            KubernetesTester.clients("customv1").patch_namespaced_custom_object(
                group, version, namespace, plural(kind), name, resource
            )
        except Exception:
            print("Failed to update a resource ({}): \n {}".format(sys.exc_info()[0], resource))
            raise
        print("Updated resource {} {} {}".format(kind, name, "(" + res_type + ")" if kind == "MongoDb" else ""))

    @staticmethod
    def delete(section, namespace):
        "delete custom object"
        delete_name = section.get("delete_name")
        loaded_yaml = yaml.safe_load(open(fixture(section["file"])))

        resource = None
        if delete_name is None:
            resource = loaded_yaml
        else:
            # remove the element by name in the case of a list of elements
            resource = [res for res in loaded_yaml if res["metadata"]["name"] == delete_name][0]

        name, kind, group, version, _ = get_crd_meta(resource)

        KubernetesTester.delete_custom_resource(namespace, name, kind, group, version)

    @staticmethod
    def delete_custom_resource(namespace, name, kind, group="mongodb.com", version="v1"):
        print("Deleting resource {} {}".format(kind, name))

        KubernetesTester.namespace = namespace
        KubernetesTester.name = name
        KubernetesTester.kind = kind

        del_options = KubernetesTester.clients("client").V1DeleteOptions()

        KubernetesTester.clients("customv1").delete_namespaced_custom_object(
            group, version, namespace, plural(kind), name, body=del_options
        )
        print("Deleted resource {} {}".format(kind, name))

    @staticmethod
    def noop(section, namespace):
        "noop action"
        pass

    @staticmethod
    def get_namespaced_custom_object(namespace, name, kind, group="mongodb.com", version="v1"):
        return KubernetesTester.clients("customv1").get_namespaced_custom_object(
            group, version, namespace, plural(kind), name
        )

    @staticmethod
    def get_resource():
        """Assumes a single resource in the test environment"""
        return KubernetesTester.get_namespaced_custom_object(
            KubernetesTester.namespace, KubernetesTester.name, KubernetesTester.kind
        )

    @staticmethod
    def in_error_state():
        return KubernetesTester.check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Failed",
        )

    @staticmethod
    def in_updated_state():
        return KubernetesTester.check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Updated",
        )

    @staticmethod
    def in_pending_state():
        return KubernetesTester.check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Pending",
        )

    @staticmethod
    def in_running_state():
        return KubernetesTester.check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Running",
        )

    @staticmethod
    def in_failed_state():
        return KubernetesTester.check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Failed",
        )

    @staticmethod
    def wait_for_status_message(rule):
        timeout = int(rule.get("timeout", INFINITY))

        def wait_for_status():
            res = KubernetesTester.get_namespaced_custom_object(
                KubernetesTester.namespace, KubernetesTester.name, KubernetesTester.kind
            )
            expected_message = rule["wait_for_message"]
            message = res.get("status", {}).get("message", "")
            if isinstance(expected_message, re.Pattern):
                return expected_message.match(message)
            return expected_message in message

        return KubernetesTester.wait_until(wait_for_status, timeout)

    @staticmethod
    def is_deleted(namespace, name, kind="MongoDB"):
        try:
            KubernetesTester.get_namespaced_custom_object(namespace, name, kind)
            return False
        except ApiException:  # ApiException is thrown when the object does not exist
            return True

    @staticmethod
    def check_phase(namespace, kind, name, phase):
        resource = KubernetesTester.get_namespaced_custom_object(namespace, name, kind)
        if "status" not in resource:
            return False
        if resource["metadata"]["generation"] != resource["status"]["observedGeneration"]:
            # If generations don't match - we're observing a previous state and the Operator
            # hasn't managed to take action yet.
            return False
        return resource["status"]["phase"] == phase

    @classmethod
    def wait_condition(cls, action):
        """Waits for a condition to occur before proceeding,
        or for some amount of time, both can appear in the file,
        will always wait for the condition and then for some amount of time.
        """
        if "wait_until" not in action:
            return

        print("Waiting until {}".format(action["wait_until"]))
        wait_phases = [a.strip() for a in action["wait_until"].split(",") if a != ""]
        for phase in wait_phases:
            # Will wait for each action passed as a , separated list
            # waiting on average the same amount of time for each phase
            # totaling `timeout`
            cls.wait_until(phase, int(action.get("timeout", 0)) / len(wait_phases))

    @classmethod
    def wait_until(cls, action, timeout=0, **kwargs):
        func = None
        # if passed a function directly, we can use it
        if callable(action):
            func = action
        else:  # otherwise find a function of that name
            func = getattr(cls, action)
        return run_periodically(func, timeout=timeout, **kwargs)

    @classmethod
    def wait_for_condition_string(cls, condition):
        """Waits for a given condition from the cluster
        Example:
        1. statefulset/my-replica-set -> status.current_replicas == 5
        """
        type_, name, attribute, expected_value = parse_condition_str(condition)

        if type_ not in ["sts", "statefulset"]:
            raise NotImplementedError("Only StatefulSets can be tested with condition strings for now")

        return cls.wait_for_condition_stateful_set(cls.get_namespace(), name, attribute, expected_value)

    @classmethod
    def wait_for_condition_stateful_set(cls, namespace, name, attribute, expected_value):
        appsv1 = KubernetesTester.clients("appsv1")
        namespace = KubernetesTester.get_namespace()
        ready_to_go = False
        while not ready_to_go:
            try:
                sts = appsv1.read_namespaced_stateful_set(name, namespace)
                ready_to_go = get_nested_attribute(sts, attribute) == expected_value
            except ApiException:
                pass

            if ready_to_go:
                return

            time.sleep(0.5)

    def setup_method(self):
        self.client = client
        self.corev1 = client.CoreV1Api()
        self.appsv1 = client.AppsV1Api()
        self.certificates = client.CertificatesV1Api()
        self.customv1 = client.CustomObjectsApi()
        self.namespace = KubernetesTester.get_namespace()
        self.name = None
        self.kind = None

    @staticmethod
    def create_group(org_id, group_name):
        """
        Creates the group with specified name and organization id in Ops Manager, returns its ID
        """
        url = build_om_groups_endpoint(KubernetesTester.get_om_base_url())
        response = KubernetesTester.om_request("post", url, {"name": group_name, "orgId": org_id})

        return response.json()["id"]

    @staticmethod
    def ensure_group(org_id, group_name):
        try:
            return KubernetesTester.get_om_group_id(group_name=group_name, org_id=org_id)
        except Exception as e:
            print(f"Caught exception: {e}")
            return KubernetesTester.create_group(org_id, group_name)

    @staticmethod
    def query_group(group_name, org_id=None):
        """
        Obtains the group id of the group with specified name.
        Note, that the logic used imitates the logic used by the Operator, 'getByName' returns all groups in all
        organizations which may be inconvenient for local development as may result in "may groups exist" exception
        """
        if org_id is None:
            # If no organization is passed, then look for all organizations
            org_id = KubernetesTester.find_organizations(group_name)

        if not isinstance(org_id, list):
            org_id = [org_id]

        if len(org_id) != 1:
            raise Exception('{} organizations with name "{}" found instead of 1!'.format(len(org_id), group_name))

        group_ids = KubernetesTester.find_groups_in_organization(org_id[0], group_name)
        if len(group_ids) != 1:
            raise Exception(
                f'{len(group_ids)} groups with name "{group_name}" found inside organization "{org_id[0]}" instead of 1!'
            )
        url = build_om_group_endpoint(KubernetesTester.get_om_base_url(), group_ids[0])
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def remove_group(group_id):
        url = build_om_group_endpoint(KubernetesTester.get_om_base_url(), group_id)
        KubernetesTester.om_request("delete", url)

    @staticmethod
    def remove_group_by_name(group_name):
        orgid = KubernetesTester.get_om_org_id()
        project_id = KubernetesTester.query_group(group_name, orgid)["id"]
        KubernetesTester.remove_group(project_id)

    @staticmethod
    def create_organization(org_name):
        """
        Creates the organization with specified name in Ops Manager, returns its ID
        """
        url = build_om_org_endpoint(KubernetesTester.get_om_base_url())
        response = KubernetesTester.om_request("post", url, {"name": org_name})

        return response.json()["id"]

    @staticmethod
    def find_organizations(org_name):
        """
        Finds all organization with specified name, iterates over max 200 pages to find all matching organizations
        (aligned with 'ompaginator.TraversePages').

        If the Organization ID has been defined, return that instead. This is required to avoid 500's in Cloud Manager.
        Returns the list of ids.
        """
        org_id = KubernetesTester.get_om_org_id()
        if org_id is not None:
            return [org_id]

        ids = []
        page = 1
        while True:
            url = build_om_org_list_endpoint(KubernetesTester.get_om_base_url(), page)
            json = KubernetesTester.om_request("get", url).json()

            # Add organization id if its name is the searched one
            ids.extend([org["id"] for org in json["results"] if org["name"] == org_name])

            if not any(link["rel"] == "next" for link in json["links"]):
                break
            page += 1

        return ids

    @staticmethod
    def remove_organization(org_id):
        """
        Removes the organization with specified id from Ops Manager
        """
        url = build_om_one_org_endpoint(KubernetesTester.get_om_base_url(), org_id)
        KubernetesTester.om_request("delete", url)

    @staticmethod
    def get_groups_in_organization_first_page(org_id):
        """
        :return: the first page of groups  (100 items for OM 4.0 and 500 for OM 4.1)
        """
        url = build_om_groups_in_org_endpoint(KubernetesTester.get_om_base_url(), org_id, 1)
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def find_groups_in_organization(org_id, group_name):
        """
        Finds all group with specified name, iterates over max 200 pages to find all matching groups inside the
        organization (aligned with 'ompaginator.TraversePages')
        Returns the list of ids.
        """

        max_pages = 200
        ids = []
        for i in range(1, max_pages):
            url = build_om_groups_in_org_endpoint(KubernetesTester.get_om_base_url(), org_id, i)
            json = KubernetesTester.om_request("get", url).json()
            # Add group id if its name is the searched one
            ids.extend([group["id"] for group in json["results"] if group["name"] == group_name])

            if not any(link["rel"] == "next" for link in json["links"]):
                break

        if len(ids) == 0:
            print(
                "Group name {} not found in organization with id {} (in {} pages)".format(group_name, org_id, max_pages)
            )

        return ids

    @staticmethod
    def get_automation_config(group_id=None, group_name=None):
        if group_id is None:
            group_id = KubernetesTester.get_om_group_id(group_name=group_name)

        url = build_automation_config_endpoint(KubernetesTester.get_om_base_url(), group_id)
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def get_automation_status(group_id=None, group_name=None):
        if group_id is None:
            group_id = KubernetesTester.get_om_group_id(group_name=group_name)

        url = build_automation_status_endpoint(KubernetesTester.get_om_base_url(), group_id)
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def get_automation_status(group_id=None, group_name=None):
        if group_id is None:
            group_id = KubernetesTester.get_om_group_id(group_name=group_name)

        url = build_automation_status_endpoint(KubernetesTester.get_om_base_url(), group_id)
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def get_monitoring_config(group_id=None):
        if group_id is None:
            group_id = KubernetesTester.get_om_group_id()
        url = build_monitoring_config_endpoint(KubernetesTester.get_om_base_url(), group_id)
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def get_backup_config(group_id=None):
        if group_id is None:
            group_id = KubernetesTester.get_om_group_id()
        url = build_backup_config_endpoint(KubernetesTester.get_om_base_url(), group_id)
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def put_automation_config(config):
        url = build_automation_config_endpoint(KubernetesTester.get_om_base_url(), KubernetesTester.get_om_group_id())
        response = KubernetesTester.om_request("put", url, config)

        return response

    @staticmethod
    def put_monitoring_config(config, group_id=None):
        if group_id is None:
            group_id = KubernetesTester.get_om_group_id()
        url = build_monitoring_config_endpoint(KubernetesTester.get_om_base_url(), group_id)
        response = KubernetesTester.om_request("put", url, config)

        return response

    @staticmethod
    def get_hosts():
        url = build_hosts_endpoint(KubernetesTester.get_om_base_url(), KubernetesTester.get_om_group_id())
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def om_request(method, endpoint, json_object=None):
        headers = {"Content-Type": "application/json"}
        auth = build_auth(KubernetesTester.get_om_user(), KubernetesTester.get_om_api_key())

        response = requests.request(method, endpoint, auth=auth, headers=headers, json=json_object)

        if response.status_code >= 300:
            raise Exception(
                "Error sending request to Ops Manager API. {} ({}).\n Request details: {} {} (data: {})".format(
                    response.status_code, response.text, method, endpoint, json_object
                )
            )

        return response

    @staticmethod
    def om_version() -> Optional[Dict[str, str]]:
        "Gets the X-MongoDB-Service-Version"
        response = KubernetesTester.om_request(
            "get",
            "{}/api/public/v1.0/groups".format(KubernetesTester.get_om_base_url()),
        )

        version = response.headers.get("X-MongoDB-Service-Version")
        if version is None:
            return None

        return dict(attr.split("=", 1) for attr in version.split("; "))

    @staticmethod
    def check_om_state_cleaned():
        """Checks that OM state is cleaned: Automation config is empty, monitoring hosts are removed"""

        config = KubernetesTester.get_automation_config()
        assert len(config["replicaSets"]) == 0, "ReplicaSets not empty: {}".format(config["replicaSets"])
        assert len(config["sharding"]) == 0, "Sharding not empty: {}".format(config["sharding"])
        assert len(config["processes"]) == 0, "Processes not empty: {}".format(config["processes"])

        hosts = KubernetesTester.get_hosts()
        assert len(hosts["results"]) == 0, "Hosts not empty: ({} hosts left)".format(len(hosts["results"]))

    @staticmethod
    def is_om_state_cleaned():
        config = KubernetesTester.get_automation_config()
        hosts = KubernetesTester.get_hosts()

        return (
            len(config["replicaSets"]) == 0
            and len(config["sharding"]) == 0
            and len(config["processes"]) == 0
            and len(hosts["results"]) == 0
        )

    @staticmethod
    def mongo_resource_deleted(check_om_state=True):
        # First we check that the MDB resource is removed
        # This depends on global state set by "create_custom_resouce", this means
        # that it can't be called independently, or, calling the remove function without
        # calling the "create" function first.
        # Should not depend in the global state of KubernetesTester

        deleted_in_k8 = KubernetesTester.is_deleted(
            KubernetesTester.namespace, KubernetesTester.name, KubernetesTester.kind
        )

        # Then we check that the resource was removed in Ops Manager if specified
        return deleted_in_k8 if not check_om_state else (deleted_in_k8 and KubernetesTester.is_om_state_cleaned())

    @staticmethod
    def mongo_resource_deleted_no_om():
        """
        Waits until the MDB resource dissappears but won't wait for OM state to be removed, as sometimes
        OM will just fail on us and make the test fail.
        """
        return KubernetesTester.mongo_resource_deleted(False)

    @staticmethod
    def build_mongodb_uri_for_rs(hosts):
        return "mongodb://{}".format(",".join(hosts))

    @staticmethod
    def random_k8s_name(prefix="test-"):
        """Deprecated: user kubetester.random_k8s_name instead."""
        return prefix + "".join(random.choice(string.ascii_lowercase) for _ in range(5))

    @staticmethod
    def random_om_project_name() -> str:
        """Generates the name for the projects with our common namespace (and project) convention so that
        GC process could remove it if it's left for some reasons. Always has a whitespace.
        """
        current_seconds_epoch = int(time.time())
        prefix = f"a-{current_seconds_epoch}-"

        return "{} {}".format(
            KubernetesTester.random_k8s_name(prefix),
            KubernetesTester.random_k8s_name(""),
        )

    @staticmethod
    def run_command_in_pod_container(
        pod_name: str,
        namespace: str,
        cmd: List[str],
        container: str = "mongodb-enterprise-database",
        api_client: Optional[kubernetes.client.ApiClient] = None,
    ) -> str:
        api_client = client.CoreV1Api(api_client=api_client)
        api_response = stream(
            api_client.connect_get_namespaced_pod_exec,
            pod_name,
            namespace,
            container=container,
            command=cmd,
            stdout=True,
            stderr=True,
        )
        return api_response

    @staticmethod
    def copy_file_inside_pod(pod_name, src_path, dest_path, namespace="default"):
        """
        This function copies a file inside the pod from localhost. (Taken from: https://stackoverflow.com/questions/59703610/copy-file-from-pod-to-host-by-using-kubernetes-python-client)
        :param api_instance: coreV1Api()
        :param name: pod name
        :param ns: pod namespace
        :param source_file: Path of the file to be copied into pod
        """

        api_client = client.CoreV1Api()
        try:
            exec_command = ["tar", "xvf", "-", "-C", "/"]
            api_response = stream(
                api_client.connect_get_namespaced_pod_exec,
                pod_name,
                namespace,
                command=exec_command,
                stderr=True,
                stdin=True,
                stdout=True,
                tty=False,
                _preload_content=False,
            )

            with tempfile.TemporaryFile() as tar_buffer:
                with tarfile.open(fileobj=tar_buffer, mode="w") as tar:
                    tar.add(src_path, dest_path)

                tar_buffer.seek(0)
                commands = []
                commands.append(tar_buffer.read())

                while api_response.is_open():
                    api_response.update(timeout=1)
                    if api_response.peek_stdout():
                        print("STDOUT: %s" % api_response.read_stdout())
                    if api_response.peek_stderr():
                        print("STDERR: %s" % api_response.read_stderr())
                    if commands:
                        c = commands.pop(0)
                        api_response.write_stdin(c.decode())
                    else:
                        break
                api_response.close()
        except ApiException as e:
            raise Exception("Failed to copy file to the pod: {}".format(e))

    @staticmethod
    def approve_certificate(name: str):
        warnings.warn(
            DeprecationWarning(
                "KubernetesTester.approve_certificate is deprecated, use kubetester.certs.approve_certificate instead!"
            )
        )
        # TODO: remove this method entirely
        from kubetester.certs import approve_certificate

        return approve_certificate(name)

    def generate_certfile(
        self,
        csr_name: str,
        certificate_request_fixture: str,
        server_pem_fixture: str,
        namespace: Optional[str] = None,
    ):
        """
        generate_certfile create a temporary file object that is created from a certificate request fixture
        as well as a fixture containing the server pem key. This file can be used to pass to a MongoClient
        when using MONGODB-X509 authentication

        :param csr_name: The name of the CSR that is to be created
        :param certificate_request_fixture: a fixture containing the contents of the certificate request
        :param server_pem_fixture: a fixture containing the server pem key file
        :return: A File object containing the key and certificate
        """
        with open(fixture(certificate_request_fixture), "r") as f:
            encoded_request = b64encode(f.read().encode("utf-8")).decode("utf-8")

        if namespace is None:
            namespace = self.namespace

        csr_body = client.V1CertificateSigningRequest(
            metadata=client.V1ObjectMeta(name=csr_name, namespace=namespace),
            spec=client.V1CertificateSigningRequestSpec(
                groups=["system:authenticated"],
                usages=["digital signature", "key encipherment", "client auth"],
                request=encoded_request,
            ),
        )

        client.CertificatesV1Api().create_certificate_signing_request(csr_body)
        self.approve_certificate(csr_name)
        wait_for_certs_to_be_issued([csr_name])
        csr = client.CertificatesV1Api().read_certificate_signing_request(csr_name)
        certificate = b64decode(csr.status.certificate)

        tmp = tempfile.NamedTemporaryFile()
        with open(fixture(server_pem_fixture), "r+b") as f:
            key = f.read()
        tmp.write(key)
        tmp.write(certificate)
        tmp.flush()

        return tmp

    @staticmethod
    def list_storage_class() -> List[client.V1StorageClass]:
        """Returns a list of all the Storage classes in this cluster."""
        return KubernetesTester.clients("storagev1").list_storage_class().items

    @staticmethod
    def get_storage_class_provisioner_enabled() -> str:
        """Returns 'a' provisioner that is known to exist in this cluster."""
        # If there's no storageclass in this cluster, then the following
        # will raise a KeyError.
        return KubernetesTester.list_storage_class()[0].provisioner

    @staticmethod
    def create_storage_class(name: str, provisioner: Optional[str] = None) -> None:
        """Creates a new StorageClass which is a duplicate of an existing one."""
        if provisioner is None:
            provisioner = KubernetesTester.get_storage_class_provisioner_enabled()

        sc0 = KubernetesTester.list_storage_class()[0]

        sc = client.V1StorageClass(
            metadata=client.V1ObjectMeta(
                name=name,
                annotations={"storageclass.kubernetes.io/is-default-class": "true"},
            ),
            provisioner=provisioner,
            volume_binding_mode=sc0.volume_binding_mode,
            reclaim_policy=sc0.reclaim_policy,
        )
        KubernetesTester.clients("storagev1").create_storage_class(sc)

    @staticmethod
    def storage_class_make_not_default(name: str):
        """Changes the 'default' annotation from a storage class."""
        sv1 = KubernetesTester.clients("storagev1")
        sc = sv1.read_storage_class(name)
        sc.metadata.annotations["storageclass.kubernetes.io/is-default-class"] = "false"
        sv1.patch_storage_class(name, sc)

    @staticmethod
    def make_default_gp2_storage_class():
        """
        gp2 is an aws-ebs storage class, make sure to only use that on aws based tests
        """
        classes = KubernetesTester.list_storage_class()

        for sc in classes:
            if sc.metadata.name == "gp2":
                # The required class already exist, no need to create it.
                return

        KubernetesTester.create_storage_class("gp2")
        KubernetesTester.storage_class_make_not_default("standard")

    @staticmethod
    def yield_existing_csrs(csr_names, timeout=300):
        warnings.warn(
            DeprecationWarning(
                "KubernetesTester.yield_existing_csrs is deprecated, use kubetester.certs.yield_existing_csrs instead!"
            )
        )
        # TODO: remove this method entirely
        from kubetester.certs import yield_existing_csrs

        return yield_existing_csrs(csr_names, timeout)

    # TODO eventually replace all usages of this function with "ReplicaSetTester(mdb_resource, 3).assert_connectivity()"
    @staticmethod
    def wait_for_rs_is_ready(hosts, wait_for=60, check_every=5, ssl=False):
        "Connects to a given replicaset and wait a while for a primary and secondaries."
        client = KubernetesTester.check_hosts_are_ready(hosts, ssl)

        check_times = wait_for / check_every

        while (client.primary is None or len(client.secondaries) < len(hosts) - 1) and check_times >= 0:
            time.sleep(check_every)
            check_times -= 1

        return client.primary, client.secondaries

    @staticmethod
    def check_hosts_are_ready(hosts, ssl=False):
        mongodburi = KubernetesTester.build_mongodb_uri_for_rs(hosts)
        options = {}
        if ssl:
            options = {"ssl": True, "tlsCAFile": SSL_CA_CERT}
        client = pymongo.MongoClient(mongodburi, **options, serverSelectionTimeoutMs=300000)

        # The ismaster command is cheap and does not require auth.
        client.admin.command("ismaster")

        return client

    def _get_pods(self, podname, qty=3):
        return [podname.format(i) for i in range(qty)]

    @staticmethod
    def check_single_pvc(
        namespace: str,
        volume,
        expected_name,
        expected_claim_name,
        expected_size,
        storage_class=None,
        labels: Optional[Dict[str, str]] = None,
        api_client: Optional[client.ApiClient] = None,
    ):
        assert volume.name == expected_name
        assert volume.persistent_volume_claim.claim_name == expected_claim_name

        pvc = client.CoreV1Api(api_client=api_client).read_namespaced_persistent_volume_claim(
            expected_claim_name, namespace
        )
        assert pvc.status.phase == "Bound"
        assert pvc.spec.resources.requests["storage"] == expected_size

        assert getattr(pvc.spec, "storage_class_name") == storage_class
        if labels is not None:
            pvc_labels = pvc.metadata.labels
            for k in labels:
                assert k in pvc_labels and pvc_labels[k] == labels[k]

    @staticmethod
    def get_mongo_server_sans(host: str) -> List[str]:
        cert_bytes = ssl.get_server_certificate((host, 27017)).encode("ascii")
        cert = x509.load_pem_x509_certificate(cert_bytes, default_backend())
        ext = cert.extensions.get_extension_for_class(x509.SubjectAlternativeName)
        return ext.value.get_values_for_type(x509.DNSName)

    @staticmethod
    def get_csr_sans(csr_name: str) -> List[str]:
        """
        Return all of the subject alternative names for a given Kubernetes
        certificate signing request.
        """
        csr = client.CertificatesV1Api().read_certificate_signing_request_status(csr_name)
        base64_csr_request = csr.spec.request
        csr_pem_string = b64decode(base64_csr_request)
        csr = x509.load_pem_x509_csr(csr_pem_string, default_backend())
        ext = csr.extensions.get_extension_for_class(x509.SubjectAlternativeName)
        return ext.value.get_values_for_type(x509.DNSName)


# Some general functions go here


def get_group(doc):
    return doc["apiVersion"].split("/")[0]


def get_version(doc):
    return doc["apiVersion"].split("/")[1]


def get_kind(doc):
    return doc["kind"]


def get_name(doc):
    return doc["metadata"]["name"]


def get_type(doc):
    return doc.get("spec", {}).get("type")


def get_crd_meta(doc):
    return get_name(doc), get_kind(doc), get_group(doc), get_version(doc), get_type(doc)


def plural(name):
    """Returns the plural of the name, in the case of `mongodb` the plural is the same."""
    return plural_map[name]


def parse_condition_str(condition):
    """
    Returns a condition into constituent parts:
    >>> parse_condition_str('sts/my-replica-set -> status.current_replicas == 3')
    >>> 'sts', 'my-replica-set', '{.status.currentReplicas}', '3'
    """
    type_name, condition = condition.split("->")
    type_, name = type_name.split("/")
    type_ = type_.strip()
    name = name.strip()

    test, expected = condition.split("==")
    test = test.strip()
    expected = expected.strip()
    if expected.isdigit():
        expected = int(expected)

    return type_, name, test, expected


def get_nested_attribute(obj, attrs):
    """Returns the `attrs` attribute descending into this object following the . notation.
    Assume you have a class Some() and:
    >>> class Some: pass
    >>> a = Some()
    >>> b = Some()
    >>> c = Some()
    >>> a.b = b
    >>> b.c = c
    >>> c.my_string = 'hello!'
    >>> get_nested_attribute(a, 'b.c.my_string')
    'hello!'
    """

    attrs = list(reversed(attrs.split(".")))
    while attrs:
        obj = getattr(obj, attrs.pop())

    return obj


def current_milliseconds():
    return int(round(time.time() * 1000))


def run_periodically(fn, *args, **kwargs):
    """
    Calls `fn` until it succeeds or until the `timeout` is reached, every `sleep_time` seconds.
    If `timeout` is negative or zero, it never times out.
    Callable fn can return single bool (condition result) or tuple[bool, str] ([condition result, status message]).

    >>> run_periodically(lambda: time.sleep(5), timeout=3, sleep_time=2)
    False
    >>> run_periodically(lambda: time.sleep(2), timeout=5, sleep_time=2)
    True
    """
    sleep_time = kwargs.get("sleep_time", SLEEP_TIME)
    timeout = kwargs.get("timeout", INFINITY)
    msg = kwargs.get("msg", None)

    start_time = current_milliseconds()
    end = start_time + (timeout * 1000)
    callable_name = fn.__name__
    attempts = 0

    while current_milliseconds() < end or timeout <= 0:
        attempts += 1
        fn_result = fn()
        fn_condition_msg = None
        if isinstance(fn_result, bool):
            fn_condition = fn_result
        elif isinstance(fn_result, tuple) and len(fn_result) == 2:
            fn_condition = fn_result[0]
            fn_condition_msg = fn_result[1]
        else:
            raise Exception("Invalid fn return type. Fn have to return either bool or a tuple[bool, str].")

        if fn_condition:
            print(
                "{} executed successfully after {} seconds and {} attempts".format(
                    callable_name, (current_milliseconds() - start_time) / 1000, attempts
                )
            )
            return True
        if msg is not None:
            condition_msg = f": {fn_condition_msg}" if fn_condition_msg is not None else ""
            print(f"waiting for {msg}{condition_msg}...")
        time.sleep(sleep_time)

    raise AssertionError(
        "Timed out executing {} after {} seconds and {} attempt(s)".format(
            callable_name, (current_milliseconds() - start_time) / 1000, attempts
        )
    )


def get_env_var_or_fail(name):
    """
    Gets a configuration option from an Environment variable. If not found, will try to find
    this option in one of the configuration files for the user.
    """
    value = os.getenv(name)

    if value is None:
        raise ValueError("Environment variable `{}` needs to be set.".format(name))

    if isinstance(value, str):
        value = value.strip()

    return value


def build_auth(user, api_key):
    return HTTPDigestAuth(user, api_key)


def build_agent_auth(group_id, api_key):
    return HTTPBasicAuth(group_id, api_key)


def build_om_groups_endpoint(base_url):
    return "{}/api/public/v1.0/groups".format(base_url)


def build_om_group_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}".format(base_url, group_id)


def build_om_org_endpoint(base_url):
    return "{}/api/public/v1.0/orgs".format(base_url)


def build_om_org_list_endpoint(base_url: string, page_num: int):
    return "{}/api/public/v1.0/orgs?itemsPerPage=500&pageNum={}".format(base_url, page_num)


def build_om_org_list_by_name_endpoint(base_url: string, name: string):
    return "{}/api/public/v1.0/orgs?name={}".format(base_url, name)


def build_om_one_org_endpoint(base_url, org_id):
    return "{}/api/public/v1.0/orgs/{}".format(base_url, org_id)


def build_om_groups_in_org_endpoint(base_url, org_id, page_num):
    return "{}/api/public/v1.0/orgs/{}/groups?itemsPerPage=500&pageNum={}".format(base_url, org_id, page_num)


def build_om_groups_in_org_by_name_endpoint(base_url: string, org_id: string, name: string):
    return "{}/api/public/v1.0/orgs/{}/groups?name={}".format(base_url, org_id, name)


def build_automation_config_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}/automationConfig".format(base_url, group_id)


def build_automation_status_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}/automationStatus".format(base_url, group_id)


def build_monitoring_config_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}/automationConfig/monitoringAgentConfig".format(base_url, group_id)


def build_backup_config_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}/automationConfig/backupAgentConfig".format(base_url, group_id)


def build_hosts_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}/hosts".format(base_url, group_id)


def ensure_nested_objects(resource: CustomObject, keys: List[str]):
    curr_dict = resource
    for k in keys:
        if k not in curr_dict:
            curr_dict[k] = {}
        curr_dict = curr_dict[k]


def fixture(filename):
    """
    Returns a relative path to a filename in one of the fixture's directories
    """
    root_dir = os.path.join(os.path.dirname(os.path.dirname(__file__)), "tests")

    fixture_dirs = []

    for dirpath, dirnames, filenames in os.walk(root_dir):
        if dirpath.endswith("/fixtures"):
            fixture_dirs.append(dirpath)

    found = None
    for dirs in fixture_dirs:
        full_path = os.path.join(dirs, filename)
        if os.path.exists(full_path) and os.path.isfile(full_path):
            if found is not None:
                warnings.warn("Fixtures with the same name were found: {}".format(full_path))
            found = full_path

    if found is None:
        raise Exception("Fixture file {} not found".format(filename))

    return found


def build_list_of_hosts(
    mdb_resource,
    namespace,
    members,
    servicename=None,
    clustername: str = "cluster.local",
    port=27017,
):
    if servicename is None:
        servicename = "{}-svc".format(mdb_resource)

    return [
        build_host_fqdn(hostname(mdb_resource, idx), namespace, servicename, clustername, port)
        for idx in range(members)
    ]


def build_host_fqdn(
    hostname: str,
    namespace: str,
    servicename: str,
    clustername: str = "cluster.local",
    port=27017,
) -> str:
    return "{hostname}.{servicename}.{namespace}.svc.{clustername}:{port}".format(
        hostname=hostname,
        servicename=servicename,
        namespace=namespace,
        clustername=clustername,
        port=port,
    )


def build_svc_fqdn(service: str, namespace: str, clustername: str = "cluster.local") -> str:
    return "{}.{}.svc.{}".format(service, namespace, clustername)


def hostname(hostname, idx):
    return "{}-{}".format(hostname, idx)


def get_pods(podname_format, qty=3):
    return [podname_format.format(i) for i in range(qty)]


def decode_secret(data: Dict[str, str]) -> Dict[str, str]:
    return {k: b64decode(v).decode("utf-8") for (k, v) in data.items()}


def validation_reason_from_exception(exception_msg):
    reasons = [
        ("in body is required", "FieldValueRequired"),
        ("in body should be one of", "FieldValueNotSupported"),
        ("in body must be of type", "FieldValueInvalid"),
    ]

    for reason in reasons:
        if reason[0] in exception_msg:
            return reason[1]


def create_testing_namespace(
    evergreen_task_id: str,
    name: str,
    api_client: Optional[kubernetes.client.ApiClient] = None,
    istio_label: Optional[bool] = False,
) -> str:
    """creates the namespace that is used by the test. Marks it with necessary labels and annotations so that
    it would be handled by configuration scripts correctly (cluster cleaner, dumping the diagnostics information)
    """

    labels = {"evg": "task"}
    if istio_label:
        labels.update({"istio-injection": "enabled"})

    annotations = {"evg/task": f"https://evergreen.mongodb.com/task/{evergreen_task_id}"}

    from kubetester import create_or_update_namespace

    create_or_update_namespace(name, labels, annotations, api_client=api_client)

    return name


def fcv_from_version(version: str) -> str:
    parsed_version = semver.VersionInfo.parse(version)
    return f"{parsed_version.major}.{parsed_version.minor}"


def ensure_ent_version(mdb_version: str) -> str:
    if "-ent" not in mdb_version:
        return mdb_version + "-ent"
    return mdb_version


@TRACER.start_as_current_span("wait_processes_ready")
def wait_processes_ready():
    # Get current automation status
    def processes_are_ready():
        auto_status = KubernetesTester.get_automation_status()
        goal_version = auto_status.get("goalVersion")

        logger.info(f"Checking if all processes have reached goal version: {goal_version}")
        processes_not_ready = []
        for process in auto_status.get("processes", []):
            process_name = process.get("name", "unknown")
            process_version = process.get("lastGoalVersionAchieved")
            if process_version != goal_version:
                logger.info(f"Process {process_name} at version {process_version}, expected {goal_version}")
                processes_not_ready.append(process_name)

        all_processes_ready = len(processes_not_ready) == 0
        if all_processes_ready:
            logger.info("All processes have reached the goal version")
        else:
            logger.info(f"{len(processes_not_ready)} processes have not yet reached the goal version")

        return all_processes_ready

    timeout = 600  # 5 minutes timeout
    KubernetesTester.wait_until(
        processes_are_ready,
        timeout=timeout,
        sleep_time=5,
    )

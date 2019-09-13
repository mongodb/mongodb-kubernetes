import os
import random
import string
import sys
import time
import warnings
from base64 import b64decode
from datetime import datetime, timezone

from typing import Dict

import jsonpatch
import pymongo
import pytest
import requests
import yaml
from kubernetes import client, config
from kubernetes.client.rest import ApiException
from requests.auth import HTTPDigestAuth

SSL_CA_CERT = "/var/run/secrets/kubernetes.io/serviceaccount/..data/ca.crt"
ENVIRONMENT_FILES = ("~/.operator-dev/om", "~/.operator-dev/contexts/{}")
ENVIRONMENT_FILE_CURRENT = os.path.expanduser("~/.operator-dev/current")

plural_map = {
    "MongoDB": "mongodb",
    "MongoDBUser": "mongodbusers",
    "MongoDBOpsManager": "opsmanagers"
}


def running_locally():
    return os.getenv("POD_NAME", "local") == "local"


skip_if_local = pytest.mark.skipif(running_locally(), reason="Only run in Kubernetes cluster")
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
        config_map = cls.clients('client').V1ConfigMap(
            metadata=cls.clients('client').V1ObjectMeta(name=name),
            data=data
        )
        cls.clients('corev1').create_namespaced_config_map(namespace, config_map)

    @classmethod
    def patch_config_map(cls, namespace, name, data):
        """Patch a config map in a given namespace with the given name and data."""
        config_map = cls.clients('client').V1ConfigMap(data=data)
        cls.clients("corev1").patch_namespaced_config_map(name, namespace, config_map)

    @classmethod
    def create_secret(cls, namespace: str, name: str, data: Dict[str, str]):
        """Create a secret in a given namespace with the given name and data—handles base64 encoding."""
        secret = cls.clients('client').V1Secret(
            metadata=cls.clients('client').V1ObjectMeta(name=name),
            string_data=data
        )
        cls.clients('corev1').create_namespaced_secret(namespace, secret)

    @classmethod
    def update_secret(cls, namespace: str, name: str, data: Dict[str, str]):
        """Updates a secret in a given namespace with the given name and data—handles base64 encoding."""
        secret = cls.clients('client').V1Secret(
            metadata=cls.clients('client').V1ObjectMeta(name=name),
            string_data=data
        )
        cls.clients('corev1').patch_namespaced_secret(name, namespace, secret)

    @classmethod
    def delete_secret(cls, namespace: str, name: str):
        """Delete a secret in a given namespace with the given name."""
        cls.clients('corev1').delete_namespaced_secret(name, namespace)

    @classmethod
    def read_secret(cls, namespace: str, name: str) -> Dict[str, str]:
        data = cls.clients('corev1').read_namespaced_secret(name, namespace).data
        return {k: b64decode(v).decode("utf-8") for (k, v) in data.items()}

    @classmethod
    def read_configmap(cls, namespace: str, name: str) -> Dict[str, str]:
        """Reads a ConfigMap and returns its contents"""
        return cls.clients("corev1").read_namespaced_config_map(name, namespace).data

    @classmethod
    def create_configmap(cls, namespace: str, name: str, data: Dict[str, str]):
        """Create a ConfigMap in a given namespace with the given name and data—handles base64 encoding."""
        configmap = cls.clients('client').V1ConfigMap(
            metadata=cls.clients('client').V1ObjectMeta(name=name),
            data=data
        )
        cls.clients('corev1').create_namespaced_config_map(namespace, configmap)

    @classmethod
    def update_configmap(cls, namespace: str, name: str, data: Dict[str, str]):
        """Updates a ConfigMap in a given namespace with the given name and data—handles base64 encoding."""
        configmap = cls.clients('client').V1ConfigMap(
            metadata=cls.clients('client').V1ObjectMeta(name=name),
            data=data
        )
        cls.clients('corev1').patch_namespaced_config_map(name, namespace, configmap)

    @classmethod
    def delete_configmap(cls, namespace: str, name: str):
        """Delete a ConfigMap in a given namespace with the given name."""
        cls.clients('corev1').delete_namespaced_config_map(name, namespace)

    @classmethod
    def create_namespace(cls, namespace_name):
        """Create a namespace with the given name."""
        namespace = cls.clients('client').V1Namespace(
            metadata=cls.clients('client').V1ObjectMeta(name=namespace_name)
        )
        cls.clients('corev1').create_namespace(namespace)

    @classmethod
    def delete_namespace(cls, name):
        """Delete the specified namespace."""
        cls.clients('corev1').delete_namespace(name, body=cls.clients("client").V1DeleteOptions())

    @staticmethod
    def clients(name):
        return {
            "client": client,
            "corev1": client.CoreV1Api(),
            "appsv1": client.AppsV1Api(),
            "storagev1": client.StorageV1Api(),
            "customv1": client.CustomObjectsApi(),
            "certificates": client.CertificatesV1beta1Api(),
            "namespace": KubernetesTester.get_namespace(),
        }[name]

    @classmethod
    def teardown_class(cls):
        "Tears down testing class, make sure pytest ends after tests are run."
        cls.teardown_env()
        sys.stdout.flush()

    @classmethod
    def setup_class(cls):
        "Will setup class (initialize kubernetes objects)"
        print('\n')
        KubernetesTester.load_configuration()
        # Loads the subclass doc
        if cls.__doc__:
            test_setup = yaml.safe_load(cls.__doc__)
            cls.prepare(test_setup, KubernetesTester.get_namespace())

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
        return get_env_var_or_fail("PROJECT_NAMESPACE")

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
    def get_om_group_id():
        # doing some "caching" for the group id on the first invocation
        if KubernetesTester.group_id is None:
            group_name = KubernetesTester.get_om_group_name()

            org_id = KubernetesTester.get_om_org_id()

            group = KubernetesTester.query_group(group_name, org_id)

            KubernetesTester.group_id = group["id"]

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

                # We wait for some time until checking the condition. This is important for updates: the resource was
                # in "running" state, it got updated and it gets to "reconciling" and to "running" again.
                # TODO ideally we need to check for the sequence of phases, e.g. "reconciling" -> "running" and remove the
                # timeout
                time.sleep(5)

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
            patch=section.get("patch", None))

    @staticmethod
    def create_many(section, namespace):
        "creates multiple custom objects from a yaml list"
        resources = yaml.safe_load(open(fixture(section["file"])))
        for res in resources:
            name, kind = KubernetesTester.create_custom_resource_from_object(
                namespace,
                res,
                exception_reason=section.get("exception", None),
                patch=section.get("patch", None))

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
        name, kind = KubernetesTester.create_custom_resource_from_object(
            namespace, resource, exception_reason, patch
        )
        KubernetesTester.namespace = namespace
        KubernetesTester.name = name
        KubernetesTester.kind = kind

    @staticmethod
    def create_custom_resource_from_object(namespace, resource, exception_reason=None, patch=None):
        name, kind, group, version, res_type = get_crd_meta(resource)
        if patch:
            patch = jsonpatch.JsonPatch.from_string(patch)
            resource = patch.apply(resource)

        KubernetesTester.namespace = namespace
        KubernetesTester.name = name
        KubernetesTester.kind = kind

        # For some long-running actions (e.g. creation of OpsManager) we may want to reuse already existing CR
        if os.getenv("SKIP_EXECUTION") is not None:
            print("Skipping creation as 'SKIP_EXECUTION' env variable is not empty")
            return

        print('Creating resource {} {} {}'.format(kind, name, '(' + res_type + ')' if kind == 'MongoDb' else ''))

        # TODO move "wait for exception" logic to a generic function and reuse for create/update/delete
        try:
            KubernetesTester.clients("customv1").create_namespaced_custom_object(
                group, version, namespace, plural(kind), resource
            )
            if exception_reason:
                raise AssertionError("Expected ApiException, but create operation succeeded!")

        except ApiException as e:
            if exception_reason:
                assert e.reason == exception_reason, "Real exception is: {}".format(e.reason)
                print('"{}" exception raised while creating the resource - this is expected!'.format(e.reason))
                return None, None

            print("Failed to create a resource ({}): \n {}".format(e, resource))
            raise

        print('Created resource {} {} {}'.format(kind, name, '(' + res_type + ')' if kind == 'MongoDb' else ''))
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
            patch = jsonpatch.JsonPatch.from_string(patch)
            resource = patch.apply(resource)

        try:
            # TODO currently if the update doesn't pass (e.g. patch is incorrect) - we don't fail here...
            KubernetesTester.clients("customv1").patch_namespaced_custom_object(
                group, version, namespace, plural(kind), name, resource
            )
        except Exception:
            print("Failed to update a resource ({}): \n {}".format(sys.exc_info()[0], resource))
            raise
        print('Updated resource {} {} {}'.format(kind, name, '(' + res_type + ')' if kind == 'MongoDb' else ''))

    @staticmethod
    def delete(section, namespace):
        "delete custom object"
        delete_name = section.get('delete_name')
        loaded_yaml = yaml.safe_load(open(fixture(section["file"])))

        resource = None
        if delete_name is None:
            resource = loaded_yaml
        else:
            # remove the element by name in the case of a list of elements
            resource = [res for res in loaded_yaml if res['metadata']['name'] == delete_name][0]

        name, kind, group, version, _ = get_crd_meta(resource)

        KubernetesTester.delete_custom_resource(namespace, name, kind, group, version)

    @staticmethod
    def delete_custom_resource(namespace, name, kind, group='mongodb.com', version='v1'):
        print('Deleting resource {} {}'.format(kind, name))

        KubernetesTester.namespace = namespace
        KubernetesTester.name = name
        KubernetesTester.kind = kind

        del_options = KubernetesTester.clients("client").V1DeleteOptions()

        KubernetesTester.clients("customv1").delete_namespaced_custom_object(
            group, version, namespace, plural(kind), name, del_options
        )
        print('Deleted resource {} {}'.format(kind, name))

    @staticmethod
    def noop(section, namespace):
        "noop action"
        pass

    @staticmethod
    def get_namespaced_custom_object(namespace, name, kind, group="mongodb.com", version="v1"):
        return KubernetesTester.clients("customv1").get_namespaced_custom_object(
            group,
            version,
            namespace,
            plural(kind),
            name
        )

    @staticmethod
    def get_resource():
        """Assumes a single resource in the test environment"""
        return KubernetesTester.get_namespaced_custom_object(
            KubernetesTester.namespace,
            KubernetesTester.name,
            KubernetesTester.kind,
        )

    @staticmethod
    def in_error_state():
        return KubernetesTester.check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Failed"
        )

    @staticmethod
    def in_running_state():
        """ Returns true if the resource in Running state, fails fast if got into Failed error.
         This allows to fail fast in case of cascade failures """
        resource = KubernetesTester.get_resource()
        if 'status' not in resource:
            return False
        phase = resource['status']['phase']

        # TODO we need to implement a more reliable mechanism to diagnose problems in the cluster. So
        # far we just ignore the "Pending" errors below, but they could be caused by real problems - not
        # just by long starting containers. Some ideas: we could check the conditions for pods to see if there
        # are errors
        intermediate_events = (
            # In this case the operator will be waiting for the StatefulSet to be in full running state
            # which under some circumstances, might not be the case if, for instance, there are too many
            # pods to start, which will be concluded after a few reconciliation passes.
            "Statefulset or its pods failed to reach READY state",
            # After agents have been installed, they might have not finished or reached goal state yet.
            "haven't reached READY",
            "Some agents failed to register"
        )

        if phase == "Failed":
            msg = resource['status']['message']
            # Sometimes (for sharded cluster for example) the Automation agents don't get on time - we
            # should survive this

            found = False
            for event in intermediate_events:
                if event in msg:
                    found = True

            if not found:
                raise AssertionError('Got into Failed phase while waiting for Running! ("{}")'.format(msg))

        return phase == "Running"

    @staticmethod
    def in_running_state_failures_possible():
        return KubernetesTester.check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Running"
        )

    @staticmethod
    def in_failed_state():
        return KubernetesTester.check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Failed"
        )

    @staticmethod
    def wait_for_status_message(rule):
        timeout = int(rule.get("timeout", INFINITY))

        def wait_for_status():
            res = KubernetesTester.get_namespaced_custom_object(KubernetesTester.namespace, KubernetesTester.name,
                                                                KubernetesTester.kind)
            return rule["wait_for_message"] in res.get('status', {}).get('message', "")

        return KubernetesTester.wait_until(wait_for_status, timeout)

    @staticmethod
    def is_deleted(namespace, name, kind="MongoDB"):
        try:
            KubernetesTester.get_namespaced_custom_object(
                namespace,
                name,
                kind
            )
            return False
        except ApiException:  # ApiException is thrown when the object does not exist
            return True

    @staticmethod
    def check_phase(namespace, kind, name, phase):
        resource = KubernetesTester.get_namespaced_custom_object(namespace, name, kind)
        if 'status' not in resource:
            return False
        return resource['status']['phase'] == phase

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
    def wait_until(cls, action, timeout=0):
        func = None
        # if passed a function directly, we can use it
        if callable(action):
            func = action
        else:  # otherwise find a function of that name
            func = getattr(cls, action)
        return run_periodically(func, timeout=timeout)

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
        self.certificates = client.CertificatesV1beta1Api()
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
        response = KubernetesTester.om_request("post", url, {'name': group_name, 'orgId': org_id})

        return response.json()["id"]

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
                '{} groups with name "{}" found inside organization "{}" instead of 1!'.format(len(org_id), org_id[0],
                                                                                               group_name))

        url = build_om_group_endpoint(KubernetesTester.get_om_base_url(),
                                      group_ids[0])
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def remove_group(group_id):
        url = build_om_group_endpoint(KubernetesTester.get_om_base_url(),
                                      group_id)
        KubernetesTester.om_request("delete", url)

    @staticmethod
    def create_organization(org_name):
        """
        Creates the organization with specified name in Ops Manager, returns its ID
        """
        url = build_om_org_endpoint(KubernetesTester.get_om_base_url())
        response = KubernetesTester.om_request("post", url, {'name': org_name})

        return response.json()["id"]

    @staticmethod
    def find_organizations(org_name):
        """
        Finds all organization with specified name, iterates over max 200 pages to find all matching organizations
        (aligned with 'ompaginator.TraversePages').

        If the Organization ID has been defined, return that instead. This is required to avoid 500's in Cloud Manager.
        Returns the list of ids
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
            print("Group name {} not found in organization with id {} (in {} pages)".format(group_name, org_id,
                                                                                            max_pages))

        return ids

    @staticmethod
    def get_automation_config(group_id=None):
        if group_id is None:
            group_id = KubernetesTester.get_om_group_id()

        url = build_automation_config_endpoint(KubernetesTester.get_om_base_url(), group_id)
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
    def put_automation_config(config):
        url = build_automation_config_endpoint(KubernetesTester.get_om_base_url(),
                                               KubernetesTester.get_om_group_id())
        response = KubernetesTester.om_request("put", url, config)

        return response

    @staticmethod
    def put_monitoring_config(config, group_id=None):
        if group_id is None:
            group_id = KubernetesTester.get_om_group_id()
        url = build_monitoring_config_endpoint(KubernetesTester.get_om_base_url(),
                                               group_id)
        response = KubernetesTester.om_request("put", url, config)

        return response

    @staticmethod
    def get_hosts():
        url = build_hosts_endpoint(KubernetesTester.get_om_base_url(),
                                   KubernetesTester.get_om_group_id())
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

        return len(config["replicaSets"]) == 0 and \
               len(config["sharding"]) == 0 and \
               len(config["processes"]) == 0 and \
               len(hosts["results"]) == 0

    @staticmethod
    def mongo_resource_deleted(check_om_state=True):
        # First we check that the MDB resource is removed
        # This depends on global state set by "create_custom_resouce", this means
        # that it can't be called independently, or, calling the remove function without
        # calling the "create" function first.
        # Should not depend in the global state of KubernetesTester

        deleted_in_k8 = KubernetesTester.is_deleted(KubernetesTester.namespace,
                                                    KubernetesTester.name,
                                                    KubernetesTester.kind)

        # Then we check that the resource was removed in Ops Manager if specified
        return deleted_in_k8 if not check_om_state else (deleted_in_k8 and KubernetesTester.is_om_state_cleaned())

    @staticmethod
    def mongo_resource_deleted_no_om():
        """
        Waits until the MDB resource dissappears but won't wait for OM state to be removed, as sometimes
        OM will just fail on us and make the test fail.
        """
        return KubernetesTester.mongo_resource_deleted(False)

    def build_mongodb_uri_for_rs(self, hosts):
        return "mongodb://{}".format(",".join(hosts))

    @staticmethod
    def random_k8s_name(prefix='test-'):
        return prefix + ''.join(
            random.choice(string.ascii_lowercase) for _ in range(10)
        )

    def approve_certificate(self, name):
        body = self.certificates.read_certificate_signing_request_status(name)
        conditions = self.client.V1beta1CertificateSigningRequestCondition(
            last_update_time=datetime.now(timezone.utc).astimezone(),
            message='This certificate was approved by E2E testing framework',
            reason='E2ETestingFramework',
            type='Approved')

        body.status.conditions = [conditions]
        self.certificates.replace_certificate_signing_request_approval(name, body)

    def yield_existing_csrs(self, csr_names, timeout=300):
        total_csrs = len(csr_names)
        seen_csrs = 0
        stop_time = time.time() + timeout

        while time.time() < stop_time:
            for idx, csr_name in enumerate(csr_names):
                try:
                    self.certificates.read_certificate_signing_request_status(csr_name)
                    # The certificate exists
                    seen_csrs += 1
                    yield csr_names.pop(idx)
                    if seen_csrs == total_csrs:
                        return  # we are done yielding all results
                    break

                except ApiException:
                    time.sleep(0.5)

        # we didn't find all of the expected csrs after the timeout period
        raise AssertionError(
            f"Expected to find {total_csrs} csrs, but only found {seen_csrs} after {timeout} seconds. Expected csrs {csr_names}")

    # TODO eventually replace all usages of this function with "ReplicaSetTester(mdb_resource, 3).assert_connectivity()"
    def wait_for_rs_is_ready(self, hosts, wait_for=60, check_every=5, ssl=False):
        "Connects to a given replicaset and wait a while for a primary and secondaries."
        client = self.check_hosts_are_ready(hosts, ssl)

        check_times = wait_for / check_every

        while (
                (client.primary is None
                 or len(client.secondaries) < len(hosts) - 1)
                and check_times >= 0
        ):
            time.sleep(check_every)
            check_times -= 1

        return client.primary, client.secondaries

    def check_hosts_are_ready(self, hosts, ssl=False):
        mongodburi = self.build_mongodb_uri_for_rs(hosts)
        options = {}
        if ssl:
            options = {
                "ssl": True,
                "ssl_ca_certs": SSL_CA_CERT
            }
        client = pymongo.MongoClient(mongodburi, **options)

        # The ismaster command is cheap and does not require auth.
        client.admin.command("ismaster")

        return client

    def _get_pods(self, podname, qty=3):
        return [podname.format(i) for i in range(qty)]

    def check_single_pvc(self, volume, expected_name, expected_claim_name, expected_size, storage_class=None):
        assert volume.name == expected_name
        assert volume.persistent_volume_claim.claim_name == expected_claim_name

        pvc = self.corev1.read_namespaced_persistent_volume_claim(
            expected_claim_name, self.namespace
        )
        assert pvc.status.phase == "Bound"
        assert pvc.spec.resources.requests["storage"] == expected_size

        assert getattr(pvc.spec, "storage_class_name") == storage_class


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
    return doc.get('spec', {}).get('type')


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

    >>> run_periodically(lambda: time.sleep(5), timeout=3, sleep_time=2)
    False
    >>> run_periodically(lambda: time.sleep(2), timeout=5, sleep_time=2)
    True
    """
    sleep_time = kwargs.get("sleep_time", SLEEP_TIME)
    timeout = kwargs.get("timeout", INFINITY)

    start_time = current_milliseconds()
    end = start_time + (timeout * 1000)
    callable_name = fn.__name__

    while current_milliseconds() < end or timeout <= 0:
        if fn():
            print('{} executed successfully after {} seconds'.format(
                callable_name,
                (current_milliseconds() - start_time) / 1000))
            return True

        time.sleep(sleep_time)

    raise AssertionError("Timed out executing {} after {} seconds".format(
        callable_name,
        (current_milliseconds() - start_time) / 1000))


def get_env_var_or_fail(name):
    """
    Gets a configuration option from an Environment variable. If not found, will try to find
    this option in one of the configuration files for the user.
    """
    value = os.getenv(name)

    if value is None and running_locally():
        # If the environment variable is not found, and in local mode,
        # look for it in any of the "environment" files
        value = get_env_var_from_file(name)

    if value is None:
        raise ValueError("Environment variable `{}` needs to be set.".format(name))

    if isinstance(value, str):
        value = value.strip()

    return value


def get_current_dev_context():
    with open(ENVIRONMENT_FILE_CURRENT) as fd:
        return fd.readline().strip()


def get_env_var_from_file(var_name):
    for env_file in ENVIRONMENT_FILES:
        if "{}" in env_file:
            env_file = env_file.format(get_current_dev_context())

        with open(os.path.expanduser(env_file)) as fd:
            for line in fd.readlines():
                try:
                    name, value = line.split("=")
                    if "export " in name:
                        _, name = name.split("export ")
                    if name.strip() == var_name:
                        return value
                except ValueError:
                    # unpack error at some point
                    continue


def build_auth(user, api_key):
    return HTTPDigestAuth(user, api_key)


def build_om_groups_endpoint(base_url):
    return "{}/api/public/v1.0/groups".format(base_url)


def build_om_group_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}".format(base_url, group_id)


def build_om_org_endpoint(base_url):
    return "{}/api/public/v1.0/orgs".format(base_url)


def build_om_org_list_endpoint(base_url, page_num):
    return "{}/api/public/v1.0/orgs?itemsPerPage=500&pageNum={}".format(base_url, page_num)


def build_om_one_org_endpoint(base_url, org_id):
    return "{}/api/public/v1.0/orgs/{}".format(base_url, org_id)


def build_om_groups_in_org_endpoint(base_url, org_id, page_num):
    return "{}/api/public/v1.0/orgs/{}/groups?itemsPerPage=500&pageNum={}".format(base_url, org_id, page_num)


def build_automation_config_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}/automationConfig".format(base_url, group_id)


def build_monitoring_config_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}/automationConfig/monitoringAgentConfig".format(base_url, group_id)


def build_hosts_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}/hosts".format(base_url, group_id)


def fixture(filename):
    """
    Returns a relative path to a filename in one of the fixture's directories
    """
    root_dir = os.path.join(os.path.dirname(os.path.dirname(__file__)), "tests")

    fixture_dirs = []

    for (dirpath, dirnames, filenames) in os.walk(root_dir):
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


def build_list_of_hosts(mdb_resource, namespace, members, servicename=None):
    if servicename is None:
        servicename = "{}-svc".format(mdb_resource)

    return [
        build_host_fqdn(hostname(mdb_resource, idx), namespace, servicename)
        for idx in range(members)
    ]


def build_host_fqdn(hostname: str, namespace: str, servicename: str, clustername: str = "cluster.local") -> str:
    return "{hostname}.{servicename}.{namespace}.{clustername}:27017".format(
        hostname=hostname, servicename=servicename, namespace=namespace, clustername=clustername
    )


def build_svc_fqdn(service: str, namespace: str, clustername: str = "cluster.local") -> str:
    return "{}.{}.svc.{}".format(service, namespace, clustername)


def hostname(hostname, idx):
    return "{}-{}".format(hostname, idx)

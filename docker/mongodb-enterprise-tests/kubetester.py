import random
import string
import sys
import time
from os import getenv

import jsonpatch
import pymongo
import requests
import yaml
from kubernetes import client, config
from kubernetes.client.rest import ApiException
from requests.auth import HTTPDigestAuth


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

    @classmethod
    def teardown_env(cls):
        """Optionally override this in a test instance to destroy the test environment."""

    @classmethod
    def create_config_map(cls, namespace, name, data):
        """Create a config map in a given namespace with the given name and data."""
        config_map = cls.clients('client').V1ConfigMap(
            metadata=cls.clients('client').V1ObjectMeta(name=name),
            data=data
        )
        cls.clients('corev1').create_namespaced_config_map(namespace, config_map)

    @classmethod
    def create_secret(cls, namespace, name, data):
        """Create a secret in a given namespace with the given name and data—handles base64 encoding."""
        secret = cls.clients('client').V1Secret(
            metadata=cls.clients('client').V1ObjectMeta(name=name),
            string_data=data
        )
        cls.clients('corev1').create_namespaced_secret(namespace, secret)

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
            "namespace": KubernetesTester.get_namespace(),
        }[name]

    @classmethod
    def teardown_class(cls):
        cls.teardown_env()

    @classmethod
    def setup_class(cls):
        "Will setup class (initialize kubernetes objects)"
        # TODO uncomment when CLOUDP-37451 is done
        # try:
        #     # removing the group in OM if it existed before running the test (could happen if running locally using 'make e2e')
        #     KubernetesTester.remove_group(KubernetesTester.get_om_group_id())
        #     print('Removed group {} from Ops Manager'.format(KubernetesTester.get_om_group_id()), flush=True)
        #
        #     # Need to nulify the cached group_id as the new group will be created
        #     KubernetesTester.group_id = None
        # except:
        #     pass

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
    def get_om_group_id():
        # doing some "caching" for the group id on the first invocation
        if KubernetesTester.group_id is None:
            KubernetesTester.group_id = KubernetesTester.query_group_id(KubernetesTester.get_om_group_name())
        return KubernetesTester.group_id

    @classmethod
    def prepare(cls, test_setup, namespace):
        allowed_actions = ["create", "update", "delete", "noop"]

        for action in [action for action in allowed_actions if action in test_setup]:
            rules = test_setup[action]
            KubernetesTester.execute(action, rules, namespace)
            cls.wait_condition(rules)

    @staticmethod
    def execute(action, rules, namespace):
        "Execute function with name `action` with arguments `rules` and `namespace`"
        getattr(KubernetesTester, action)(rules, namespace)

    @staticmethod
    def wait_for(seconds):
        "Will wait for a given amount of seconds."
        time.sleep(int(seconds))

    @staticmethod
    def create(section, namespace):
        "creates a custom object from filename"
        resource = yaml.safe_load(open(section["file"]))

        KubernetesTester.create_custom_resource_from_object(
            namespace,
            resource,
            exception_reason=section.get("exception", None),
            patch=section.get("patch", None),
        )

    @staticmethod
    def create_custom_resource_from_object(namespace, resource, exception_reason=None, patch=None):
        name, kind, group, version = get_crd_meta(resource)
        if patch:
            patch = jsonpatch.JsonPatch.from_string(patch)
            resource = patch.apply(resource)

        KubernetesTester.namespace = namespace
        KubernetesTester.name = name
        KubernetesTester.kind = kind

        print('Creating resource {} {}'.format(kind, name), flush=True)

        # todo move "wait for exception" logic to a generic function and reuse for create/update/delete
        try:
            KubernetesTester.clients("customv1").create_namespaced_custom_object(
                group, version, namespace, plural(kind), resource
            )
            if exception_reason:
                raise AssertionError("Expected the ApiException, but create operation succeeded!")

        except ApiException as e:
            if exception_reason:
                assert e.reason == exception_reason, "Real exception is: {}".format(e.reason)
                print('"{}" exception raised while creating the resource - this is expected!'.format(e.reason), flush=True)
                return
            else:
                print("Failed to create a resource ({}): \n {}".format(e, resource), flush=True)
                raise

        print('Created resource {} {}'.format(kind, name), flush=True)

    @staticmethod
    def update(section, namespace):
        "patches custom object"
        resource = yaml.safe_load(open(section["file"]))
        name, kind, group, version = get_crd_meta(resource)
        patch = jsonpatch.JsonPatch.from_string(section["patch"])
        patched = patch.apply(resource)

        print('Updating resource {} {} ({})'.format(kind, name, patch), flush=True)

        try:
            KubernetesTester.clients("customv1").patch_namespaced_custom_object(
                group, version, namespace, plural(kind), name, patched
            )
        except Exception:
            print("Failed to update a resource ({}): \n {}".format(sys.exc_info()[0], patched), flush=True)
            raise
        print('Updated resource {} {}'.format(kind, name), flush=True)

    @staticmethod
    def delete(section, namespace):
        "delete custom object"
        resource = yaml.safe_load(open(section["file"]))
        name, kind, group, version = get_crd_meta(resource)
        del_options = KubernetesTester.clients("client").V1DeleteOptions()

        print('Deleting resource {} {}'.format(kind, name), flush=True)

        KubernetesTester.clients("customv1").delete_namespaced_custom_object(
            group, version, namespace, plural(kind), name, del_options
        )
        print('Deleted resource {} {}'.format(kind, name), flush=True)

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
        return KubernetesTester._check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Failed"
        )

    @staticmethod
    def in_running_state():
        return KubernetesTester._check_phase(
            KubernetesTester.namespace,
            KubernetesTester.kind,
            KubernetesTester.name,
            "Running"
        )

    @staticmethod
    def is_deleted():
        try:
            KubernetesTester.get_namespaced_custom_object(
                KubernetesTester.namespace,
                KubernetesTester.name,
                KubernetesTester.kind
            )
            return False
        except ApiException:  # ApiException is thrown when the object does not exist
            return True

    @staticmethod
    def _check_phase(namespace, kind, name, phase):
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
        if "wait_until" not in action and "wait_for" not in action:
            return
        print('Waiting for the condition: {}'.format(action), flush=True)
        sys.stdout.flush()

        if "wait_until" in action:
            print("Waiting until {}".format(action["wait_until"]), flush=True)
            cls.wait_until(action["wait_until"], int(action.get("timeout", 60)))
        else:
            KubernetesTester.wait_for(action.get("timeout", 0))

    @classmethod
    def wait_until(cls, action, timeout):
        func = getattr(cls, action)
        return func_with_timeout(func, timeout)

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
        self.customv1 = client.CustomObjectsApi()
        self.namespace = KubernetesTester.get_namespace()
        self.name = None
        self.kind = None

    @staticmethod
    def query_group_id(group_name):
        """Obtains the group id from group name"""
        url = build_om_group_by_name_endpoint(KubernetesTester.get_om_base_url(),
                                              group_name)
        response = KubernetesTester.om_request("get", url)
        if response.status_code >= 300:
            raise Exception(
                "Error obtaining ID from Ops Manager API. {} {}".format(
                    response.status_code, response.text
                )
            )

        return response.json()["id"]

    @staticmethod
    def remove_group(group_id):
        url = build_om_group_delete_endpoint(KubernetesTester.get_om_base_url(),
                                             group_id)
        KubernetesTester.om_request("delete", url)
        # TODO is the exception thrown if request fails?

    @staticmethod
    def get_automation_config():
        url = build_automation_config_endpoint(KubernetesTester.get_om_base_url(),
                                               KubernetesTester.get_om_group_id())
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def get_hosts():
        url = build_hosts_endpoint(KubernetesTester.get_om_base_url(),
                                   KubernetesTester.get_om_group_id())
        response = KubernetesTester.om_request("get", url)

        return response.json()

    @staticmethod
    def om_request(method, endpoint):
        headers = {"Content-Type": "application/json"}
        auth = build_auth(KubernetesTester.get_om_user(), KubernetesTester.get_om_api_key())

        response = requests.request(method, endpoint, auth=auth, headers=headers)

        return response

    @staticmethod
    def check_om_state_cleaned():
        """Checks that OM state is cleaned: Automation config is empty, monitoring hosts are removed"""

        config = KubernetesTester.get_automation_config()
        assert len(config["replicaSets"]) == 0, "ReplicaSets not empty: {}".format(config["replicaSets"])
        assert len(config["sharding"]) == 0, "Sharding not empty: {}".format(config["sharding"])
        assert len(config["processes"]) == 0, "Processes not empty: {}".format(config["processes"])

        hosts = KubernetesTester.get_hosts()
        assert len(hosts["results"]) == 0, "Hosts not empty: {}".format(hosts["results"])

    @staticmethod
    def mongo_resource_deleted():
        return KubernetesTester.is_deleted() and func_with_assertions(KubernetesTester.check_om_state_cleaned)

    def build_mongodb_uri_for_rs(self, hosts):
        return "mongodb://{}".format(",".join(hosts))

    def build_mongodb_uri_for_sh(self, hosts):
        return "mongodb://{}".format(",".join(hosts))

    def build_mongodb_uri_for_mongos(self, host):
        return "mongodb://{}".format(host)

    @staticmethod
    def random_k8s_name():
        return 'test-' + ''.join(
            random.choice(string.ascii_lowercase) for _ in range(10)
        )

    def wait_for_rs_is_ready(self, hosts, wait_for=60, check_every=5):
        "Connects to a given replicaset and wait a while for a primary and secondaries."
        mongodburi = self.build_mongodb_uri_for_rs(hosts)
        client = pymongo.MongoClient(mongodburi)

        check_times = wait_for / check_every
        while client.primary is None and check_times >= 0:
            time.sleep(check_every)
            check_times -= 1

        return client.primary, client.secondaries

    def check_mongos_is_ready(self, host):
        mongodburi = self.build_mongodb_uri_for_mongos(host)
        client = pymongo.MongoClient(mongodburi)
        try:
            # The ismaster command is cheap and does not require auth.
            return client.admin.command("ismaster")
        except pymongo.ConnectionFailure:
            raise Exception(
                "Checking if {} `ismaster` failed. No connectivity to this host was possible.".format(
                    mongodburi
                )
            )

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


def get_crd_meta(doc):
    return get_name(doc), get_kind(doc), get_group(doc), get_version(doc)


def plural(name):
    return "{}s".format(name.lower())


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


def func_with_timeout(func, timeout=120, sleep_time=2):
    """
    >>> func_with_timeout(lambda: time.sleep(5), timeout=3, sleep_time=0)
    False
    >>> func_with_timeout(lambda: time.sleep(2), timeout=5, sleep_time=0)
    True
    """
    start_time = current_milliseconds()
    timeout_time = start_time + (timeout * 1000)
    while True:
        time_passed = current_milliseconds() - start_time
        if time_passed + start_time >= timeout_time:
            raise AssertionError("Timed out executing {} after {} seconds".format(func.__name__, timeout))
        if func():
            print('{} executed successfully after {} seconds'.format(func.__name__, time_passed / 1000), flush=True)
            return True
        time.sleep(sleep_time)


def func_with_assertions(func):
    try:
        func()
        return True
    except AssertionError as e:
        # so we know which AssertionError was raised
        print("The check for {} hasn't passed yet. {}".format(func.__name__, e), flush=True)
        return False


def get_env_var_or_fail(var_name):
    env_value = getenv(var_name)
    if not env_value:
        raise ValueError("Environment variable `{}` needs to be set.".format(var_name))
    return env_value


def build_auth(user, api_key):
    return HTTPDigestAuth(user, api_key)


def build_om_group_by_name_endpoint(base_url, name):
    return "{}/api/public/v1.0/groups/byName/{}".format(base_url, name)


def build_om_group_delete_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}".format(base_url, group_id)


def build_automation_config_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}/automationConfig".format(base_url, group_id)


def build_hosts_endpoint(base_url, group_id):
    return "{}/api/public/v1.0/groups/{}/hosts".format(base_url, group_id)

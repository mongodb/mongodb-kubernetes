from kubernetes import client, config
from kubernetes.client.rest import ApiException

import time
import yaml
from os import getenv

import requests
from requests.auth import HTTPDigestAuth
import jsonpatch
import pymongo


class KubernetesTester(object):
    """Tests a kubernetes object application."""

    @staticmethod
    def clients(name):
        return {
            "client": client,
            "corev1": client.CoreV1Api(),
            "appsv1": client.AppsV1Api(),
            "customv1": client.CustomObjectsApi(),
            "namespace": KubernetesTester.get_namespace(),
        }[name]

    @classmethod
    def setup_class(cls):
        "Will setup class (initialize kubernetes objects)"
        KubernetesTester.load_configuration()
        # will this take the subclass doc?
        test_setup = yaml.safe_load(cls.__doc__)
        KubernetesTester.prepare(test_setup, KubernetesTester.get_namespace())

    @staticmethod
    def load_configuration():
        "Loads kubernetes client configuration from kubectl config or incluster."
        try:
            config.load_kube_config()
        except Exception:
            config.load_incluster_config()

    @staticmethod
    def get_namespace():
        namespace = getenv("PROJECT_NAMESPACE")
        if not namespace:
            raise ValueError(
                "Environment variable `PROJECT_NAMESPACE` needs to be set."
            )

        return namespace

    @staticmethod
    def prepare(test_setup, namespace):
        allowed_actions = ["create", "update", "delete"]

        # gets type of action
        action = [action for action in allowed_actions if action in test_setup][0]
        rules = test_setup[action]

        KubernetesTester.execute(action, rules, namespace)
        KubernetesTester.wait_condition(rules)

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
        name, kind, group, version = get_crd_meta(resource)

        KubernetesTester.clients("customv1").create_namespaced_custom_object(
            group, version, namespace, plural(kind), resource
        )

    @staticmethod
    def wait_condition(action):
        if "wait_until" in action:
            print("Waiting until {}".format(action["wait_until"]))
            KubernetesTester.wait_until(action["wait_until"])
        elif "wait_for" in action:
            print("Waiting for {}".format(action["wait_for"]))
            KubernetesTester.wait_for(action["wait_for"])

    @staticmethod
    def wait_until(condition):
        """Waits for a given condition from the cluster
        Example:
        1. statefulset/my-replica-set -> status.current_replicas == 5
        """
        type_, name, test, expected = parse_condition_str(condition)
        appsv1 = KubernetesTester.clients("appsv1")
        namespace = KubernetesTester.get_namespace()
        ready_to_go = False

        if type_ not in ['sts', 'statefulset']:
            raise NotImplemented('Only StatefulSets can be tested for now')

        while not ready_to_go:
            try:
                sts = appsv1.read_namespaced_stateful_set(name, namespace)
                ready_to_go = get_nested_attribute(sts, test) == expected
            except ApiException:
                pass

            if ready_to_go:
                break

            time.sleep(10)

    @staticmethod
    def update(section, namespace):
        "patches custom object"
        resource = yaml.safe_load(open(section["file"]))
        name, kind, group, version = get_crd_meta(resource)
        patch = jsonpatch.JsonPatch.from_string(section["patch"])
        patched = patch.apply(resource)

        KubernetesTester.clients("customv1").patch_namespaced_custom_object(
            group, version, namespace, plural(kind), name, patched
        )

    @staticmethod
    def delete(section, namespace):
        "delete custom object"
        resource = yaml.safe_load(open(section["file"]))
        name, kind, group, version = get_crd_meta(resource)
        del_options = KubernetesTester.clients("client").V1DeleteOptions()

        KubernetesTester.clients("customv1").delete_namespaced_custom_object(
            group, version, namespace, plural(kind), name, del_options
        )

    def setup_method(self):
        self.client = client
        self.corev1 = client.CoreV1Api()
        self.appsv1 = client.AppsV1Api()
        self.customv1 = client.CustomObjectsApi()
        self.namespace = KubernetesTester.get_namespace()

    def get_group_id(self, name, om_vars):
        "Obtains the group id from group name"
        headers = {"Content-Type": "application/json"}
        endpoint = self.build_om_group_byname_endpoint(name, om_vars)
        auth = self.build_auth(om_vars["user"], om_vars["public_api_key"])
        response = requests.get(endpoint, auth=auth, headers=headers)

        return response.json()["id"]

    def om_vars(self):
        group_name = getenv("PROJECT_NAMESPACE").rstrip()
        om_vars = {
            "base_url": getenv("OM_HOST").rstrip(),
            "public_api_key": getenv("OM_API_KEY").rstrip(),
            "user": getenv("OM_USER").rstrip(),
        }
        om_vars["group_id"] = self.get_group_id(group_name, om_vars)
        return om_vars

    def build_auth(self, user, api_key):
        return HTTPDigestAuth(user, api_key)

    def build_om_group_byname_endpoint(self, name, om_vars):
        return "{}/api/public/v1.0/groups/byName/{}".format(om_vars["base_url"], name)

    def build_automation_config_endpoint(self, om_vars):
        return "{}/api/public/v1.0/groups/{}/automationConfig".format(
            om_vars["base_url"], om_vars["group_id"]
        )

    def get_automation_config(self):
        om_vars = self.om_vars()
        auth = self.build_auth(om_vars["user"], om_vars["public_api_key"])
        endpoint = self.build_automation_config_endpoint(om_vars)
        headers = {"Content-Type": "application/json"}
        response = requests.get(endpoint, auth=auth, headers=headers)

        return response.json()

    def build_mongodb_uri_for_rs(self, hosts):
        return "mongodb://{}".format(",".join(hosts))

    def wait_for_rs_is_ready(self, hosts, wait_for=60, check_every=5):
        "Connects to a given replicaset and wait a while for a primary and secondaries."
        mongodburi = self.build_mongodb_uri_for_rs(hosts)
        client = pymongo.MongoClient(mongodburi)

        check_times = wait_for / check_every
        while client.primary is None and check_times >= 0:
            time.sleep(check_every)
            check_times -= 1

        return client.primary, client.secondaries


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
    """Returns a condition into constituent parts:
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
    'hola'
    """

    attrs = list(reversed(attrs.split(".")))
    while attrs:
        obj = getattr(obj, attrs.pop())

    return obj

from typing import List, Optional, Dict

from kubeobject import CustomObject
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDBCommon, Phase, in_desired_state, MongoDB, get_pods
from kubetester.omtester import OMTester, OMContext


class MongoDBOpsManager(CustomObject, MongoDBCommon):
    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "opsmanagers",
            "kind": "MongoDBOpsManager",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(MongoDBOpsManager, self).__init__(*args, **with_defaults)

    def assert_reaches_phase(
        self, phase: Phase, msg_regexp=None, timeout=None, ignore_errors=False
    ):
        self.wait_for(
            lambda s: in_desired_state(
                self.get_om_status_phase(),
                phase,
                self.get_om_status_message(),
                msg_regexp=msg_regexp,
                ignore_errors=ignore_errors,
            ),
            timeout,
            should_raise=True,
        )
        if phase == Phase.Running:
            assert Phase[self.get_appdb_status()["phase"]] == Phase.Running

    def assert_abandons_phase(self, phase: Phase, timeout=None):
        return self.wait_for(
            lambda s: s.get_om_status_phase() != phase, timeout, should_raise=True
        )

    def assert_reaches(self, fn, timeout=None):
        return self.wait_for(fn, timeout=timeout, should_raise=True)

    def get_appdb_resource(self) -> MongoDB:
        mdb = MongoDB(name=self.app_db_name(), namespace=self.namespace)
        # We "artificially" add SCRAM authentication to make syntax match the normal MongoDB -
        # this will let the mongo_uri() method work correctly
        # (opsmanager_types.go does the same)
        mdb["spec"] = self["spec"]["applicationDatabase"]
        mdb["spec"]["type"] = MongoDB.Types.REPLICA_SET
        mdb["spec"]["security"] = {"authentication": {"modes": ["SCRAM"]}}
        return mdb

    def services(self) -> List[client.V1Service]:
        """Returns a two element list with internal and external Services.

        Any of them might be None if the Service is not found.
        """
        services = []
        service_names = (self.name + "-svc", self.name + "-svc-ext")

        for name in service_names:
            try:
                svc = client.CoreV1Api().read_namespaced_service(name, self.namespace)
                services.append(svc)
            except ApiException:
                services.append(None)

        return services[0], services[1]

    def get_statefulset(self) -> client.V1StatefulSet:
        return client.AppsV1Api().read_namespaced_stateful_set(
            self.name, self.namespace
        )

    def get_appdb_statefulset(self) -> client.V1StatefulSet:
        return client.AppsV1Api().read_namespaced_stateful_set(
            self.app_db_name(), self.namespace
        )

    def get_backup_statefulset(self) -> client.V1StatefulSet:
        return client.AppsV1Api().read_namespaced_stateful_set(
            self.backup_daemon_name(), self.namespace
        )

    def get_appdb_pods(self) -> List[client.V1Pod]:
        return [
            client.CoreV1Api().read_namespaced_pod(podname, self.namespace)
            for podname in get_pods(
                self.app_db_name() + "-{}", self.get_appdb_members_count()
            )
        ]

    def get_backup_pod(self) -> client.V1Pod:
        return client.CoreV1Api().read_namespaced_pod(
            self.backup_daemon_pod_name(), self.namespace
        )

    def get_or_create_mongodb_connection_config_map(
        self, mongodb_name: str, project_name: str
    ) -> str:
        config_map_name = f"{mongodb_name}-config"
        try:
            KubernetesTester.create_configmap(
                self.namespace,
                config_map_name,
                {"baseUrl": self.get_om_status_url(), "projectName": project_name},
            )
        except ApiException as e:
            if e.status != 409:
                raise
        return config_map_name

    def get_om_tester(self, project_name: Optional[str] = None) -> OMTester:
        """ Returns the instance of OMTester helping to check the state of Ops Manager deployed in Kubernetes. """
        api_key_secret = KubernetesTester.read_secret(
            KubernetesTester.get_namespace(), self.api_key_secret()
        )
        om_context = OMContext(
            self.get_om_status_url(),
            api_key_secret["user"],
            api_key_secret["publicApiKey"],
            project_name=project_name,
        )
        return OMTester(om_context)

    def __repr__(self):
        # FIX: this should be __unicode__
        return "MongoDBOpsManager| status: {}| message: {}".format(
            self["status"]["opsManager"].get("phase", ""),
            self["status"]["opsManager"].get("message", ""),
        )

    def get_appdb_members_count(self) -> int:
        return self["spec"]["applicationDatabase"]["members"]

    def get_status(self) -> Optional:
        if "status" not in self:
            return None
        return self["status"]

    def get_om_status(self) -> Optional:
        if self.get_status() is None:
            return None
        return self.get_status()["opsManager"]

    def get_om_status_phase(self) -> Optional[Phase]:
        if self.get_om_status() is None:
            return None
        return Phase[self.get_om_status()["phase"]]

    def get_om_status_message(self) -> Optional[str]:
        try:
            return self.get_om_status()["message"]
        except (KeyError, TypeError):
            return None

    def get_appdb_status(self) -> Optional[Dict]:
        if self.get_status() is None:
            return None
        return self.get_status()["applicationDatabase"]

    def get_om_status_last_transition(self) -> Optional[int]:
        if self.get_om_status() is None:
            return None
        return self.get_om_status()["lastTransition"]

    def get_om_status_url(self) -> Optional[str]:
        if self.get_om_status() is None or "url" not in self.get_om_status():
            return None
        return self.get_om_status()["url"]

    def api_key_secret(self) -> str:
        return self.name + "-admin-key"

    def app_db_name(self) -> str:
        return self.name + "-db"

    def app_db_password_secret_name(self) -> str:
        return self.app_db_name() + "-password"

    def backup_daemon_name(self) -> str:
        return self.name + "-backup-daemon"

    def backup_daemon_pod_name(self) -> str:
        return self.backup_daemon_name() + "-0"

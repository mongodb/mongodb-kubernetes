from __future__ import annotations

import json
import time
from typing import Any, Dict, List, Optional

from kubernetes import client
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import SLEEP_TIME, KubernetesTester, run_periodically
from kubetester.omtester import OMContext, OMTester

# Mirrors the constants in controllers/om/headless_connection.go.
HEADLESS_AUTOMATION_CONFIG_SECRET_SUFFIX = "-config"
HEADLESS_AUTOMATION_CONFIG_SECRET_KEY = "cluster-config.json"
HEADLESS_AGENT_VERSION_ANNOTATION = "agent.mongodb.com/version"

_HEADLESS_UNSUPPORTED = (
    "{method} is not available in headless mode (MDB_E2E_PROJECT_MODE=headless): the MongoDB "
    "resource is managed without Ops Manager, so there is no Ops Manager API to query. Gate the test "
    "with kubetester.omtester.skip_if_headless, or assert the equivalent state from the "
    "<name>-config Secret and the agent pods."
)


def _int_or_zero(value: Any) -> int:
    """Parses an annotation/version into an int, falling back to 0 like the operator does."""
    try:
        return int(value)
    except (TypeError, ValueError):
        return 0


class HeadlessOMTester(OMTester):
    """OMTester for resources that are not managed by Ops Manager.

    Instead of the Ops Manager HTTP API it reads the automation config from the <name>-config
    Secret and derives goal state from the agent pod annotations, exactly like the operator's
    om.HeadlessConnection.ReadAutomationStatus:

      * goalVersion is the `version` field of the automation config;
      * a process is reported only once its pod exists (pod name == process name for replica
        sets, or the process hostname), owned by the resource's StatefulSet;
      * lastGoalVersionAchieved comes from the `agent.mongodb.com/version` pod annotation and is
        0 when the annotation is missing.

    Every Ops Manager-only surface (backup, monitoring, health endpoints, feature controls,
    agent API keys, org/project lookups) raises NotImplementedError with a pointer to
    skip_if_headless.
    """

    def __init__(self, om_context: OMContext):
        if not om_context.group_name:
            raise ValueError(
                "HeadlessOMTester requires an OMContext with project_name/group_name set to the "
                "MongoDB resource name; use OMContext.build_headless(namespace, name)"
            )
        self.context = om_context
        self.namespace = om_context.namespace or KubernetesTester.get_namespace()
        self.latest_backup_completion_time = None

    ################################ automation config / goal state ################################

    def secret_name(self) -> str:
        return f"{self.context.group_name}{HEADLESS_AUTOMATION_CONFIG_SECRET_SUFFIX}"

    def _read_secret_data(self) -> Optional[Dict[str, str]]:
        """Decoded Secret data, or None when the Secret does not exist (resource deleted)."""
        try:
            return KubernetesTester.read_secret(self.namespace, self.secret_name())
        except client.rest.ApiException as e:
            if e.status == 404:
                return None
            raise

    def _read_deployment(self) -> Dict[str, Any]:
        """Mirrors HeadlessConnection.readDeployment: missing Secret/key -> empty deployment."""
        data = self._read_secret_data()
        raw = (data or {}).get(HEADLESS_AUTOMATION_CONFIG_SECRET_KEY)
        if not raw:
            return {}
        return json.loads(raw)

    def _automation_config_exists(self) -> bool:
        data = self._read_secret_data()
        return bool(data and data.get(HEADLESS_AUTOMATION_CONFIG_SECRET_KEY))

    def get_automation_config(
        self, key: str = HEADLESS_AUTOMATION_CONFIG_SECRET_KEY, timeout: int = 60
    ) -> Dict[str, Any]:
        """Returns the automation config stored in the <name>-config Secret.

        The operator writes the Secret asynchronously, so the Secret (and the config key) are
        waited for up to `timeout` seconds before failing.
        """
        deadline = time.time() + timeout
        while True:
            data = self._read_secret_data()
            if data is not None and data.get(key):
                return json.loads(data[key])
            if time.time() >= deadline:
                raise AssertionError(
                    f"automation config Secret {self.namespace}/{self.secret_name()} (key {key!r}) was not "
                    f"found within {timeout}s; expected a headless MongoDB resource that has reconciled"
                )
            time.sleep(SLEEP_TIME)

    def get_automation_config_tester(self, **kwargs) -> AutomationConfigTester:
        return AutomationConfigTester(self.get_automation_config(), **kwargs)

    def _agent_pods_by_name(self) -> Dict[str, client.V1Pod]:
        """Pods owned by this resource's StatefulSet, keyed by pod name (mirrors listAgentPods)."""
        pods = KubernetesTester.clients("corev1").list_namespaced_pod(self.namespace).items
        result: Dict[str, client.V1Pod] = {}
        for pod in pods:
            for ref in pod.metadata.owner_references or []:
                if ref.kind == "StatefulSet" and ref.name == self.context.group_name:
                    result[pod.metadata.name] = pod
                    break
        return result

    def api_get_automation_status(self) -> Dict[str, Any]:
        """Same shape as OMTester.api_get_automation_status (the OM /automationStatus response)."""
        deployment = self._read_deployment()
        goal_version = _int_or_zero(deployment.get("version"))
        pods_by_name = self._agent_pods_by_name()

        processes: List[Dict[str, Any]] = []
        for process in deployment.get("processes") or []:
            pod = pods_by_name.get(process["name"]) or pods_by_name.get(process["hostname"])
            if pod is None:
                # Before a pod exists the agent has not checked in, so it is not reported.
                continue

            annotation = (pod.metadata.annotations or {}).get(HEADLESS_AGENT_VERSION_ANNOTATION)
            processes.append(
                {
                    "name": process["name"],
                    "hostname": process["hostname"],
                    "lastGoalVersionAchieved": _int_or_zero(annotation),
                    "plan": [],
                }
            )

        return {"goalVersion": goal_version, "processes": processes}

    ########################################### hosts ############################################

    def api_get_hosts(self) -> Dict[str, Any]:
        """Mirrors the operator's HeadlessConnection.GetHosts synthesis from the automation config."""
        deployment = self._read_deployment()
        results = [
            {"id": process["name"], "hostname": process["hostname"]} for process in deployment.get("processes") or []
        ]
        return {"results": results}

    def assert_hosts_empty(self):
        # A deleted headless resource garbage-collects its Secret; no config means no hosts.
        if self._automation_config_exists():
            self.get_automation_config_tester().assert_empty()
        assert len(self.api_get_hosts()["results"]) == 0

    def wait_until_hosts_are_empty(self, timeout=30):
        run_periodically(fn=lambda: len(self.api_get_hosts()["results"]) == 0, timeout=timeout)

    def wait_until_hosts_are_not_empty(self, timeout=30):
        run_periodically(fn=lambda: len(self.api_get_hosts()["results"]) > 0, timeout=timeout)

    ################################ Ops Manager only (raise clearly) ################################

    def _unsupported(self, method: str) -> NotImplementedError:
        return NotImplementedError(_HEADLESS_UNSUPPORTED.format(method=method))

    def om_request(self, method, path, json_object: Optional[Dict] = None, retries=3, agent_endpoint=False):
        raise self._unsupported(f"om_request({method.upper()} {path})")

    def request_health(self, base_url: Optional[str] = None) -> tuple[int, str]:
        raise self._unsupported("request_health")

    def check_healthiness(self) -> tuple[int, str]:
        raise self._unsupported("check_healthiness")

    def assert_healthiness(self):
        raise self._unsupported("assert_healthiness")

    def assert_om_instances_healthiness(self, pod_urls: str):
        raise self._unsupported("assert_om_instances_healthiness")

    def assert_test_service(self):
        raise self._unsupported("assert_test_service")

    def assert_support_page_enabled(self):
        raise self._unsupported("assert_support_page_enabled")

    def query_backup(self, db_name: str, collection_name: str, timeout: int):
        raise self._unsupported("query_backup")

    def api_update_version_manifest(self, major_version: str = "8.0"):
        raise self._unsupported("api_update_version_manifest")

    def get_feature_controls(self):
        raise self._unsupported("get_feature_controls")

    def clear_feature_controls(self) -> None:
        raise self._unsupported("clear_feature_controls")

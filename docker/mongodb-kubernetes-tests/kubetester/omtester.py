from __future__ import annotations

import logging
import os
import re
import tempfile
import time
import urllib.parse
from datetime import datetime
from enum import Enum
from typing import Dict, List, Optional

import pymongo
import pytest
import requests
import semver
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import build_agent_auth, build_auth, run_periodically
from kubetester.mongotester import BackgroundHealthChecker
from kubetester.om_queryable_backups import OMQueryableBackup
from opentelemetry import trace
from requests.adapters import HTTPAdapter, Retry
from tests.common.ops_manager.cloud_manager import is_cloud_qa

from .kubetester import get_env_var_or_fail

skip_if_cloud_manager = pytest.mark.skipif(is_cloud_qa(), reason="Do not run in Cloud Manager")


class BackupStatus(str, Enum):
    """Enum for backup statuses in Ops Manager. Note that 'str' is inherited to fix json serialization issues"""

    STARTED = "STARTED"
    STOPPED = "STOPPED"
    TERMINATING = "TERMINATING"


# todo use @dataclass annotation https://www.python.org/dev/peps/pep-0557/
class OMContext(object):
    def __init__(
        self,
        base_url,
        user,
        public_key,
        project_name=None,
        project_id=None,
        org_id=None,
        agent_api_key=None,
    ):
        self.base_url = base_url
        self.project_id = project_id
        self.group_name = project_name
        self.user = user
        self.public_key = public_key
        self.org_id = org_id
        self.agent_api_key = agent_api_key

    @staticmethod
    def build_from_config_map_and_secret(
        connection_config_map: Dict[str, str], connection_secret: Dict[str, str]
    ) -> OMContext:
        if "publicApiKey" in connection_secret:
            return OMContext(
                base_url=connection_config_map["baseUrl"],
                project_id=None,
                project_name=connection_config_map["projectName"],
                org_id=connection_config_map.get("orgId", ""),
                user=connection_secret["user"],
                public_key=connection_secret["publicApiKey"],
            )
        else:
            return OMContext(
                base_url=connection_config_map["baseUrl"],
                project_id=None,
                project_name=connection_config_map["projectName"],
                org_id=connection_config_map.get("orgId", ""),
                user=connection_secret["publicKey"],
                public_key=connection_secret["privateKey"],
            )


class OMTester(object):
    """OMTester is designed to encapsulate communication with Ops Manager. It also provides the
    set of assertion methods helping to write tests"""

    def __init__(self, om_context: OMContext):
        self.context = om_context
        # we only have a group id if we also have a name
        if self.context.group_name:
            self.ensure_group_id()

        # Those are saved here so we can access om at the end of the test run and retrieve diagnostic data easily.
        if self.context.project_id:
            if os.environ.get("OM_PROJECT_ID", ""):
                os.environ["OM_PROJECT_ID"] = os.environ["OM_PROJECT_ID"] + "," + self.context.project_id
            else:
                os.environ["OM_PROJECT_ID"] = self.context.project_id
        if self.context.public_key:
            os.environ["OM_API_KEY"] = self.context.public_key
        if self.context.public_key:
            os.environ["OM_USER"] = self.context.user
        if self.context.base_url:
            os.environ["OM_HOST"] = self.context.base_url
        self.latest_backup_completion_time = None

    def ensure_group_id(self):
        if self.context.project_id is None:
            self.context.project_id = self.find_group_id()

    def get_project_events(self):
        return self.om_request("get", f"/groups/{self.context.project_id}/events")

    def create_restore_job_snapshot(self, snapshot_id: Optional[str] = None) -> str:
        """restores the mongodb cluster to some version using the snapshot. If 'snapshot_id' omitted then the
        latest snapshot will be used."""
        cluster_id = self.get_backup_cluster_id()
        if snapshot_id is None:
            snapshots = self.api_get_snapshots(cluster_id)
            snapshot_id = snapshots[-1]["id"]

        return self.api_create_restore_job_from_snapshot(cluster_id, snapshot_id)["id"]

    def set_latest_backup_completion_time(self, timestamp):
        self.latest_backup_completion_time = timestamp

    def get_latest_backup_completion_time(self):
        return self.latest_backup_completion_time or 0

    def create_restore_job_pit(self, pit_milliseconds: int, retry: int = 120):
        """creates a restore job to restore the mongodb cluster to some version specified by the parameter."""
        cluster_id = self.get_backup_cluster_id()
        while retry > 0:
            try:
                span = trace.get_current_span()
                span.set_attribute(key="mck.pit_retries", value=retry)
                self.api_create_restore_job_pit(cluster_id, pit_milliseconds)
                return
            except Exception as e:
                # this exception is usually raised for some time (some oplog slices not received or whatever)
                # but eventually is gone and restore job is started.
                if "Invalid restore point:" not in str(e):
                    raise e
            retry -= 1
            time.sleep(1)
        raise Exception("Failed to create a restore job!")

    def wait_until_backup_snapshots_are_ready(
        self,
        expected_count: int,
        timeout: int = 800,
        expected_config_count: int = 1,
        is_sharded_cluster: bool = False,
    ):
        """waits until at least 'expected_count' backup snapshots is in complete state"""
        start_time = time.time()
        cluster_id = self.get_backup_cluster_id(expected_config_count, is_sharded_cluster)

        if expected_count == 1:
            print(f"Waiting until 1 snapshot is ready (can take a while)")
        else:
            print(f"Waiting until {expected_count} snapshots are ready (can take a while)")

        initial_timeout = timeout
        while timeout > 0:
            snapshots = self.api_get_snapshots(cluster_id)
            if len([s for s in snapshots if s["complete"]]) >= expected_count:
                print(f"Snapshots are ready, project: {self.context.group_name}, time: {time.time() - start_time} sec")
                span = trace.get_current_span()
                span.set_attribute(key="mck.snapshot_time", value=time.time() - start_time)
                completed_snapshots = [s for s in snapshots if s.get("complete", False)]
                latest_snapshot = max(completed_snapshots, key=lambda s: s["created"]["date"])
                snapshot_timestamp = latest_snapshot["created"]["date"]
                print(f"Current Backup Snapshots: {snapshots}")
                self.set_latest_backup_completion_time(
                    time_to_millis(datetime.fromisoformat(snapshot_timestamp.replace("Z", "")))
                )
                return
            time.sleep(3)
            timeout -= 3

        snapshots = self.api_get_snapshots(cluster_id)
        print(f"Current Backup Snapshots: {snapshots}")

        raise Exception(
            f"Timeout ({initial_timeout}) reached while waiting for {expected_count} snapshot(s) to be ready for the "
            f"project {self.context.group_name} "
        )

    def wait_until_restore_job_is_ready(self, job_id: str, timeout: int = 1500):
        """waits until there's one finished restore job in the project"""
        start_time = time.time()
        cluster_id = self.get_backup_cluster_id()

        print(f"Waiting until restore job with id {job_id} is finished")

        initial_timeout = timeout
        while timeout > 0:
            job = self.api_get_restore_job_by_id(cluster_id, job_id)
            if job["statusName"] == "FINISHED":
                print(
                    f"Restore job is finished, project: {self.context.group_name}, time: {time.time() - start_time} sec"
                )
                return
            time.sleep(3)
            timeout -= 3

        jobs = self.api_get_restore_jobs(cluster_id)
        print(f"Current Restore Jobs: {jobs}")

        raise AssertionError(
            f"Timeout ({initial_timeout}) reached while waiting for the restore job to finish for the "
            f"project {self.context.group_name} "
        )

    def get_backup_cluster_id(self, expected_config_count: int = 1, is_sharded_cluster: bool = False) -> str:
        configs = self.api_read_backup_configs()
        assert len(configs) == expected_config_count

        if not is_sharded_cluster:
            # we can use the first config as there's only one MongoDB in deployment
            return configs[0]["clusterId"]
        # retrieve the sharded_replica_set
        clusters = self.api_get_clusters()["results"]
        for cluster in clusters:
            if cluster["typeName"] == "SHARDED_REPLICA_SET":
                return cluster["id"]

    def assert_healthiness(self):
        self.do_assert_healthiness(self.context.base_url)
        # TODO we need to check the login page as well (/user) - does it render properly?

    def assert_om_instances_healthiness(self, pod_urls: str):
        """Checks each of the OM urls for healthiness. This is different from 'assert_healthiness' which makes
        a call to the service instead"""
        for pod_fqdn in pod_urls:
            self.do_assert_healthiness(pod_fqdn)

    def assert_version(self, version: str):
        """makes the request to a random API url to get headers"""
        response = self.om_request("get", "/orgs")
        assert f"versionString={version}" in response.headers["X-MongoDB-Service-Version"]

    def assert_test_service(self):
        endpoint = self.context.base_url + "/test/utils/systemTime"
        response = requests.request("get", endpoint, verify=False)
        assert response.status_code == requests.status_codes.codes.OK

    def assert_support_page_enabled(self):
        """The method ends successfully if 'mms.helpAndSupportPage.enabled' is set to 'true'. It's 'false' by default.
        See mms SupportResource.supportLoggedOut()"""
        endpoint = self.context.base_url + "/support"
        response = requests.request("get", endpoint, allow_redirects=False, verify=False)

        # logic: if mms.helpAndSupportPage.enabled==true - then status is 307, otherwise 303"
        assert response.status_code == 307

    def assert_group_exists(self):
        path = "/groups/" + self.context.project_id
        response = self.om_request("get", path)

        assert response.status_code == requests.status_codes.codes.OK

    def assert_daemon_enabled(self, host_fqdn: str, head_db_path: str):
        encoded_head_db_path = urllib.parse.quote(head_db_path, safe="")
        response = self.om_request(
            "get",
            f"/admin/backup/daemon/configs/{host_fqdn}/{encoded_head_db_path}",
        )

        assert response.status_code == requests.status_codes.codes.OK
        daemon_config = response.json()
        assert daemon_config["machine"] == {
            "headRootDirectory": head_db_path,
            "machine": host_fqdn,
        }
        assert daemon_config["assignmentEnabled"]
        assert daemon_config["configured"]

    def _assert_stores(self, expected_stores: List[Dict], endpoint: str, store_type: str):
        response = self.om_request("get", endpoint)
        assert response.status_code == requests.status_codes.codes.OK

        existing_stores = {result["id"]: result for result in response.json()["results"]}

        assert len(expected_stores) == len(existing_stores), f"expected:{expected_stores} actual: {existing_stores}."

        for expected in expected_stores:
            store_id = expected["id"]
            assert store_id in existing_stores, f"existing {store_type} store with id {store_id} not found"
            existing = existing_stores[store_id]
            for key in expected:
                assert expected[key] == existing[key]

    def assert_oplog_stores(self, expected_oplog_stores: List):
        """verifies that the list of oplog store configs in OM is equal to the expected one"""
        self._assert_stores(expected_oplog_stores, "/admin/backup/oplog/mongoConfigs", "oplog")

    def assert_oplog_s3_stores(self, expected_oplog_s3_stores: List):
        """verifies that the list of oplog s3 store configs in OM is equal to the expected one"""
        self._assert_stores(expected_oplog_s3_stores, "/admin/backup/oplog/s3Configs", "s3")

    def assert_block_stores(self, expected_block_stores: List):
        """verifies that the list of oplog store configs in OM is equal to the expected one"""
        self._assert_stores(expected_block_stores, "/admin/backup/snapshot/mongoConfigs", "blockstore")

    def assert_s3_stores(self, expected_s3_stores: List):
        """verifies that the list of s3 store configs in OM is equal to the expected one"""
        self._assert_stores(expected_s3_stores, "/admin/backup/snapshot/s3Configs", "s3")

    def get_s3_stores(self):
        """verifies that the list of s3 store configs in OM is equal to the expected one"""
        response = self.om_request("get", "/admin/backup/snapshot/s3Configs")
        assert response.status_code == requests.status_codes.codes.OK
        return response.json()

    def get_oplog_s3_stores(self):
        """verifies that the list of s3 store configs in OM is equal to the expected one"""
        response = self.om_request("get", "/admin/backup/oplog/s3Configs")
        assert response.status_code == requests.status_codes.codes.OK
        return response.json()

    def assert_hosts_empty(self):
        self.get_automation_config_tester().assert_empty()
        hosts = self.api_get_hosts()
        assert len(hosts["results"]) == 0

    def wait_until_hosts_are_empty(self, timeout=30):
        def hosts_are_empty():
            hosts = self.api_get_hosts()["results"]
            return len(hosts) == 0

        run_periodically(fn=hosts_are_empty, timeout=timeout)

    def wait_until_hosts_are_not_empty(self, timeout=30):
        def hosts_are_not_empty():
            hosts = self.api_get_hosts()["results"]
            return len(hosts) != 0

        run_periodically(fn=hosts_are_not_empty, timeout=timeout)

    def assert_om_version(self, expected_version: str):
        assert self.api_get_om_version() == expected_version

    def check_healthiness(self) -> tuple[str, str]:
        return OMTester.request_health(self.context.base_url)

    @staticmethod
    def request_health(base_url: str) -> tuple[str, str]:
        endpoint = base_url + "/monitor/health"
        response = requests.request("get", endpoint, verify=False)
        return response.status_code, response.text

    @staticmethod
    def do_assert_healthiness(base_url: str):
        status_code, _ = OMTester.request_health(base_url)
        assert (
            status_code == requests.status_codes.codes.OK
        ), "Expected HTTP 200 from Ops Manager but got {} ({})".format(status_code, datetime.now())

    def om_request(self, method, path, json_object: Optional[Dict] = None, retries=3, agent_endpoint=False):
        """performs the digest API request to Ops Manager. Note that the paths don't need to be prefixed with
        '/api../v1.0' as the method does it internally."""
        span = trace.get_current_span()

        headers = {"Content-Type": "application/json"}
        if agent_endpoint:
            auth = build_agent_auth(self.context.project_id, self.context.agent_api_key)
            endpoint = f"{self.context.base_url}{path}"
        else:
            auth = build_auth(self.context.user, self.context.public_key)
            endpoint = f"{self.context.base_url}/api/public/v1.0{path}"

        start_time = time.time()

        session = requests.Session()
        retry = Retry(backoff_factor=5)
        adapter = HTTPAdapter(max_retries=retry)
        session.mount("http://", adapter)
        session.mount("https://", adapter)

        pattern = re.compile(r"/[a-f0-9]{24}")
        sanitized_path = pattern.sub("/{id}", path)
        span.set_attribute(key=f"mck.om.request.resource", value=sanitized_path)

        def om_request():
            try:
                response = session.request(
                    url=endpoint,
                    method=method,
                    auth=auth,
                    headers=headers,
                    json=json_object,
                    timeout=30,
                    verify=False,
                )
            except Exception as e:
                print("failed connecting to om")
                raise e

            span.set_attribute(key=f"mck.om.request.duration", value=time.time() - start_time)
            span.set_attribute(key=f"mck.om.request.fullpath", value=path)

            if response.status_code >= 300:
                raise Exception(
                    "Error sending request to Ops Manager API. {} ({}).\n Request details: {} {} (data: {})".format(
                        response.status_code, response.text, method, endpoint, json_object
                    )
                )
            return response

        retry_count = retries
        last_exception = Exception("Failed unexpectedly while retrying OM request")
        while retry_count >= 0:
            try:
                resp = om_request()
                span.set_attribute(key=f"mck.om.request.retries", value=retries - retry_count)
                return resp
            except Exception as e:
                print(f"Encountered exception: {e} on retry number {retries-retry_count}")
                span.set_attribute(key=f"mck.om.request.exception", value=str(e))
                last_exception = e
                time.sleep(1)
                retry_count -= 1

        raise last_exception

    def get_feature_controls(self):
        return self.om_request("get", f"/groups/{self.context.project_id}/controlledFeature").json()

    def find_group_id(self):
        """
        Obtains the group id of the group with specified name.
        Note, that the logic used repeats the logic used by the Operator.
        """
        if self.context.org_id is None or self.context.org_id == "":
            # If no organization is passed, then look for all organizations
            self.context.org_id = self.api_get_organization_id(self.context.group_name)
            if self.context.org_id == "":
                raise Exception(f"Organization with name {self.context.group_name} not found!")

        group_id = self.api_get_group_in_organization(self.context.org_id, self.context.group_name)
        if group_id == "":
            raise Exception(
                f"Group with name {self.context.group_name} not found in organization {self.context.org_id}!"
            )
        return group_id

    def api_backup_group(self):
        group_id = self.find_group_id()
        return self.om_request("get", f"/admin/backup/groups/{self.context.project_id}").json()

    def api_get_om_version(self) -> str:
        # This can be any API request - we just need the header in the response
        response = self.om_request("get", f"/groups/{self.context.project_id}/backupConfigs")
        version_header = response.headers["X-MongoDB-Service-Version"]
        version = version_header.split("versionString=")[1]
        parsed_version = semver.VersionInfo.parse(version)
        return f"{parsed_version.major}.{parsed_version.minor}.{parsed_version.patch}"

    def api_get_organization_id(self, org_name: str) -> str:
        encoded_org_name = urllib.parse.quote_plus(org_name)
        json = self.om_request("get", f"/orgs?name={encoded_org_name}").json()
        if len(json["results"]) == 0:
            return ""
        return json["results"][0]["id"]

    def api_get_group_in_organization(self, org_id: str, group_name: str) -> str:
        encoded_group_name = urllib.parse.quote_plus(group_name)
        json = self.om_request("get", f"/orgs/{org_id}/groups?name={encoded_group_name}").json()
        if len(json["results"]) == 0:
            return ""
        if len(json["results"]) > 1:
            for res in json["results"]:
                if res["name"] == group_name:
                    return res["id"]
            raise Exception(f"More than one groups with name {group_name} found!")
        return json["results"][0]["id"]

    def api_get_hosts(self) -> Dict:
        return self.om_request("get", f"/groups/{self.context.project_id}/hosts").json()

    def get_automation_config_tester(self, **kwargs) -> AutomationConfigTester:
        json = self.om_request("get", f"/groups/{self.context.project_id}/automationConfig").json()
        return AutomationConfigTester(json, **kwargs)

    def get_backup_config(self) -> List:
        return self.om_request("get", f"/groups/{self.context.project_id}/automationConfig/backupAgentConfig").json()

    def get_monitoring_config(self) -> List:
        return self.om_request(
            "get", f"/groups/{self.context.project_id}/automationConfig/monitoringAgentConfig"
        ).json()

    def api_read_backup_configs(self) -> List:
        return self.om_request("get", f"/groups/{self.context.project_id}/backupConfigs").json()["results"]

    # Backup states are from here:
    # https://github.com/10gen/mms/blob/bcec76f60fc10fd6b7de40ee0f57951b54a4b4a0/server/src/main/com/xgen/cloud/common/brs/_public/model/BackupConfigState.java#L8
    def wait_until_backup_deactivated(self, timeout=30, is_sharded_cluster=False, expected_config_count=1):
        def wait_until_backup_deactivated():
            found_backup = False
            cluster_id = self.get_backup_cluster_id(
                is_sharded_cluster=is_sharded_cluster,
                expected_config_count=expected_config_count,
            )
            for config in self.api_read_backup_configs():
                if config["clusterId"] == cluster_id:
                    found_backup = True
                    # Backup has been deactivated
                    if config["statusName"] in ["INACTIVE", "TERMINATING", "STOPPED"]:
                        return True
                # Backup does not exist, which we correlate with backup is deactivated
                if not found_backup:
                    return True
            return False

        run_periodically(fn=wait_until_backup_deactivated, timeout=timeout)

    def wait_until_backup_running(self, timeout=30, is_sharded_cluster=False, expected_config_count=1):
        def wait_until_backup_running():
            cluster_id = self.get_backup_cluster_id(
                is_sharded_cluster=is_sharded_cluster,
                expected_config_count=expected_config_count,
            )
            for config in self.api_read_backup_configs():
                if config["clusterId"] == cluster_id:
                    if config["statusName"] in ["STARTED", "PROVISIONING"]:
                        return True
            return False

        run_periodically(fn=wait_until_backup_running, timeout=timeout)

    def api_read_backup_snapshot_schedule(self) -> Dict:
        backup_configs = self.api_read_backup_configs()[0]
        return self.om_request(
            "get",
            f"/groups/{self.context.project_id}/backupConfigs/{backup_configs['clusterId']}/snapshotSchedule",
        ).json()

    def api_read_measurements(
        self,
        host_id: str,
        database_name: Optional[str] = None,
        project_id: Optional[str] = None,
        period: str = "P1DT12H",
    ):
        """
        Reads a measurement from the measurements and alerts API:

        https://docs.opsmanager.mongodb.com/v4.4/reference/api/measures/get-host-process-system-measurements/
        """
        database_path = ""
        if database_name is not None:
            database_path = f"/databases/{database_name}"

        return self.om_request(
            "get",
            f"/groups/{project_id}/hosts/{host_id}{database_path}/measurements?granularity=PT30S&period={period}",
        ).json()["measurements"]

    def api_read_monitoring_agents(self) -> List:
        return self._read_agents("MONITORING")

    def api_read_automation_agents(self) -> List:
        return self._read_agents("AUTOMATION")

    def _read_agents(self, agent_type: str, page_num: int = 1):
        return self.om_request(
            "get",
            f"/groups/{self.context.project_id}/agents/{agent_type}?pageNum={page_num}",
        ).json()["results"]

    def api_get_snapshots(self, cluster_id: str) -> List:
        return self.om_request("get", f"/groups/{self.context.project_id}/clusters/{cluster_id}/snapshots").json()[
            "results"
        ]

    def api_get_clusters(self) -> List:
        return self.om_request("get", f"/groups/{self.context.project_id}/clusters/").json()

    def api_create_restore_job_pit(self, cluster_id: str, pit_milliseconds: int):
        """Creates a restore job that reverts a mongodb cluster to some time defined by 'pit_milliseconds'"""
        data = self._restore_job_payload(cluster_id)
        data["pointInTimeUTCMillis"] = pit_milliseconds
        return self.om_request(
            "post",
            f"/groups/{self.context.project_id}/clusters/{cluster_id}/restoreJobs",
            data,
        )

    def api_create_restore_job_from_snapshot(self, cluster_id: str, snapshot_id: str, retry: int = 3) -> Dict:
        """
        Creates a restore job that uses an existing snapshot as the source

        The restore job might fail to be created if
        """
        data = self._restore_job_payload(cluster_id)
        data["snapshotId"] = snapshot_id

        for r in range(retry):
            try:
                result = self.om_request(
                    "post",
                    f"/groups/{self.context.project_id}/clusters/{cluster_id}/restoreJobs",
                    data,
                )
            except Exception as e:
                logging.info(e)
                logging.info(f"Could not create restore job, attempt {r + 1}")
                time.sleep((r + 1) * 10)
                continue

            return result.json()["results"][0]

        raise Exception(f"Could not create restore job after {retry} attempts")

    def api_get_restore_jobs(self, cluster_id: str) -> List:
        return self.om_request(
            "get",
            f"/groups/{self.context.project_id}/clusters/{cluster_id}/restoreJobs",
        ).json()["results"]

    def api_get_restore_job_by_id(self, cluster_id: str, id: str) -> Dict:
        return self.om_request(
            "get",
            f"/groups/{self.context.project_id}/clusters/{cluster_id}/restoreJobs/{id}",
        ).json()

    def api_remove_group(self):
        controlled_features_data = {
            "externalManagementSystem": {"name": "mongodb-kubernetes-operator"},
            "policies": [],
        }
        self.om_request(
            "put",
            f"/groups/{self.context.project_id}/controlledFeature",
            controlled_features_data,
        )
        self.om_request("put", f"/groups/{self.context.project_id}/automationConfig", {})
        return self.om_request("delete", f"/groups/{self.context.project_id}")

    def _restore_job_payload(self, cluster_id) -> Dict:
        return {
            "delivery": {
                "methodName": "AUTOMATED_RESTORE",
                "targetGroupId": self.context.project_id,
                "targetClusterId": cluster_id,
            },
        }

    def query_backup(self, db_name: str, collection_name: str, timeout: int):
        """Query the first backup snapshot and return all records from specified collection."""
        qb = OMQueryableBackup(self.context.base_url, self.context.project_id)
        connParams = qb.connection_params(timeout)

        caPem = tempfile.NamedTemporaryFile(delete=False, mode="w")
        caPem.write(connParams.ca_pem)
        caPem.flush()

        clientPem = tempfile.NamedTemporaryFile(delete=False, mode="w")
        clientPem.write(connParams.client_pem)
        clientPem.flush()

        dbClient = pymongo.MongoClient(
            host=connParams.host,
            tls=True,
            tlsCAFile=caPem.name,
            tlsCertificateKeyFile=clientPem.name,
            serverSelectionTimeoutMs=300000,
        )[db_name]
        collection = dbClient[collection_name]
        return list(collection.find())

    def api_get_preferred_hostnames(self):
        return self.om_request("get", f"/group/v2/info/{self.context.project_id}", agent_endpoint=True).json()[
            "preferredHostnames"
        ]

    def api_update_version_manifest(self, major_version: str = "8.0"):
        body = requests.get(url=f"https://opsmanager.mongodb.com/static/version_manifest/{major_version}.json").json()
        self.om_request("put", "/versionManifest", json_object=body)


class OMBackgroundTester(BackgroundHealthChecker):
    """

    Note, that it may return sporadic 500 when the appdb is being restarted, we
    won't fail because of this so checking only for
    'allowed_sequential_failures' failures. In practice having
    'allowed_sequential_failures' should work as failures are very rare (1-2 per
    appdb upgrade) but let's be safe to avoid e2e flakiness.

    """

    def __init__(
        self,
        om_tester: OMTester,
        wait_sec: int = 3,
        allowed_sequential_failures: int = 3,
    ):
        super().__init__(
            health_function=om_tester.assert_healthiness,
            wait_sec=wait_sec,
            allowed_sequential_failures=allowed_sequential_failures,
        )


# TODO can we move below methods to some other place?


def get_agent_cert_names(namespace: str) -> List[str]:
    agent_names = ["mms-automation-agent", "mms-backup-agent", "mms-monitoring-agent"]
    return ["{}.{}".format(agent_name, namespace) for agent_name in agent_names]


def get_rs_cert_names(
    mdb_resource: str,
    namespace: str,
    *,
    members: int = 3,
    with_internal_auth_certs: bool = False,
    with_agent_certs: bool = False,
) -> List[str]:
    cert_names = [f"{mdb_resource}-{i}.{namespace}" for i in range(members)]

    if with_internal_auth_certs:
        cert_names += [f"{mdb_resource}-{i}-clusterfile.{namespace}" for i in range(members)]

    if with_agent_certs:
        cert_names += get_agent_cert_names(namespace)

    return cert_names


def get_st_cert_names(
    mdb_resource: str,
    namespace: str,
    *,
    with_internal_auth_certs: bool = False,
    with_agent_certs: bool = False,
) -> List[str]:
    return get_rs_cert_names(
        mdb_resource,
        namespace,
        members=1,
        with_internal_auth_certs=with_internal_auth_certs,
        with_agent_certs=with_agent_certs,
    )


def get_sc_cert_names(
    mdb_resource: str,
    namespace: str,
    *,
    num_shards: int = 1,
    members: int = 3,
    config_members: int = 3,
    num_mongos: int = 2,
    with_internal_auth_certs: bool = False,
    with_agent_certs: bool = False,
) -> List[str]:
    names = []

    for shard_num in range(num_shards):
        for member in range(members):
            # e.g. test-tls-x509-sc-0-1.developer14
            names.append("{}-{}-{}.{}".format(mdb_resource, shard_num, member, namespace))
            if with_internal_auth_certs:
                # e.g. test-tls-x509-sc-0-2-clusterfile.developer14
                names.append("{}-{}-{}-clusterfile.{}".format(mdb_resource, shard_num, member, namespace))

    for member in range(config_members):
        # e.g. test-tls-x509-sc-config-1.developer14
        names.append("{}-config-{}.{}".format(mdb_resource, member, namespace))
        if with_internal_auth_certs:
            # e.g. test-tls-x509-sc-config-1-clusterfile.developer14
            names.append("{}-config-{}-clusterfile.{}".format(mdb_resource, member, namespace))

    for mongos in range(num_mongos):
        # e.g.test-tls-x509-sc-mongos-1.developer14
        names.append("{}-mongos-{}.{}".format(mdb_resource, mongos, namespace))
        if with_internal_auth_certs:
            # e.g. test-tls-x509-sc-mongos-0-clusterfile.developer14
            names.append("{}-mongos-{}-clusterfile.{}".format(mdb_resource, mongos, namespace))

    if with_agent_certs:
        names.extend(get_agent_cert_names(namespace))

    return names


def should_include_tag(version: Optional[Dict[str, str]]) -> bool:
    """Checks if the Ops Manager version API includes the EXTERNALLY_MANAGED tag.
    This is, the version of Ops Manager is greater or equals than 4.2.2 or Cloud
    Manager.

    """

    feature_controls_enabled_version = "4.2.2"
    if version is None:
        return True

    if "versionString" not in version:
        return True

    if re.match("^v\d+", version["versionString"]):
        # Cloud Manager supports Feature Controls
        return False

    match = re.match(r"^(\d{1,2}\.\d{1,2}\.\d{1,2}).*", version["versionString"])
    if match:
        version_string = match.group(1)

        # version_string is lower than 4.2.2
        return semver.compare(version_string, feature_controls_enabled_version) < 0

    return True


def time_to_millis(date_time) -> int:
    """https://stackoverflow.com/a/11111177/614239"""
    epoch = datetime.utcfromtimestamp(0)
    pit_millis = (date_time - epoch).total_seconds() * 1000
    return pit_millis

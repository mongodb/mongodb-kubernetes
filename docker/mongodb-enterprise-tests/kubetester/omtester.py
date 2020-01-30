from __future__ import annotations

import urllib.parse
from datetime import datetime
from typing import List, Dict

import pytest
import requests
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import build_auth
from kubetester.mongotester import BackgroundHealthChecker

from .kubetester import get_env_var_or_fail


def running_cloud_manager():
    "Determines if the current test is running against Cloud Manager"
    return get_env_var_or_fail("OM_HOST") == "https://cloud-qa.mongodb.com"


skip_if_cloud_manager = pytest.mark.skipif(
    running_cloud_manager(), reason="Do not run in Cloud Manager"
)


# todo use @dataclass annotation https://www.python.org/dev/peps/pep-0557/
class OMContext(object):
    def __init__(
        self, base_url, user, public_key, group_name=None, group_id=None, org_id=None
    ):
        self.base_url = base_url
        self.group_id = group_id
        self.group_name = group_name
        self.user = user
        self.public_key = public_key
        self.org_id = org_id

    @staticmethod
    def build_from_config_map_and_secret(
        connection_config_map: Dict[str, str], connection_secret: Dict[str, str]
    ) -> OMContext:
        return OMContext(
            base_url=connection_config_map["baseUrl"],
            group_id=None,
            group_name=connection_config_map["projectName"],
            org_id=connection_config_map["orgId"],
            user=connection_secret["user"],
            public_key=connection_secret["publicApiKey"],
        )


class OMTester(object):
    """ OMTester is designed to encapsulate communication with Ops Manager. It also provides the
    set of assertion methods helping to write tests"""

    def __init__(self, om_context: OMContext):
        self.context = om_context
        # we only have a group id if we also have a name
        if self.context.group_name:
            self.ensure_group_id()

    def ensure_group_id(self):
        if self.context.group_id is None:
            self.context.group_id = self.find_group_id()

    def assert_healthiness(self):
        self.do_assert_healthiness(self.context.base_url)
        # TODO we need to check the login page as well (/user) - does it render properly?

    def assert_om_instances_healthiness(self, pod_urls: str):
        """Checks each of the OM urls for healthiness. This is different from 'assert_healthiness' which makes
        a call to the service instead"""
        for pod_fqdn in pod_urls:
            self.do_assert_healthiness(pod_fqdn)

    def assert_version(self, version: str):
        """ makes the request to a random API url to get headers """
        response = self.om_request("get", "/orgs")
        assert (
            f"versionString={version}" in response.headers["X-MongoDB-Service-Version"]
        )

    def assert_test_service(self):
        endpoint = self.context.base_url + "/test/utils/systemTime"
        response = requests.request("get", endpoint)
        assert response.status_code == requests.status_codes.codes.OK

    def assert_support_page_enabled(self):
        """The method ends successfully if 'mms.helpAndSupportPage.enabled' is set to 'true'. It's 'false' by default.
            See mms SupportResource.supportLoggedOut()"""
        endpoint = self.context.base_url + "/support"
        response = requests.request("get", endpoint, allow_redirects=False)

        # logic: if mms.helpAndSupportPage.enabled==true - then status is 307, otherwise 303"
        assert response.status_code == 307

    def assert_group_exists(self):
        path = "/groups/" + self.context.group_id
        response = self.om_request("get", path)

        assert response.status_code == requests.status_codes.codes.OK

    def assert_daemon_enabled(self, host_name: str, head_db_path: str):
        encoded_head_db_path = urllib.parse.quote(head_db_path, safe="")
        response = self.om_request(
            "get", f"/admin/backup/daemon/configs/{host_name}/{encoded_head_db_path}",
        )

        assert response.status_code == requests.status_codes.codes.OK
        daemon_config = response.json()
        assert daemon_config["machine"] == {
            "headRootDirectory": head_db_path,
            "machine": host_name,
        }
        assert daemon_config["assignmentEnabled"]
        assert daemon_config["configured"]

    def _assert_stores(
        self, expected_stores: List[Dict], endpoint: str, store_type: str
    ):
        response = self.om_request("get", endpoint)
        assert response.status_code == requests.status_codes.codes.OK

        existing_stores = {
            result["id"]: result for result in response.json()["results"]
        }

        assert len(expected_stores) == len(
            existing_stores
        ), f"expected:{expected_stores} actual: {existing_stores}."

        for expected in expected_stores:
            store_id = expected["id"]
            assert (
                store_id in existing_stores
            ), f"existing {store_type} store with id {store_id} not found"
            existing = existing_stores[store_id]
            for key in expected:
                assert expected[key] == existing[key]

    def assert_oplog_stores(self, expected_oplog_stores: List):
        """ verifies that the list of oplog store configs in OM is equal to the expected one"""
        self._assert_stores(
            expected_oplog_stores, "/admin/backup/oplog/mongoConfigs", "oplog"
        )

    def assert_s3_stores(self, expected_s3_stores: List):
        """ verifies that the list of s3 store configs in OM is equal to the expected one"""
        self._assert_stores(
            expected_s3_stores, "/admin/backup/snapshot/s3Configs", "s3"
        )

    def assert_empty(self):
        self.get_automation_config_tester().assert_empty()
        hosts = self.api_get_hosts()
        assert len(hosts["results"]) == 0

    @staticmethod
    def do_assert_healthiness(base_url: str):
        endpoint = base_url + "/monitor/health"
        response = requests.request("get", endpoint)
        assert (
            response.status_code == requests.status_codes.codes.OK
        ), "Expected HTTP 200 from Ops Manager but got {} ({})".format(
            response.status_code, datetime.now()
        )

    def om_request(self, method, path, json_object=None):
        """ performs the digest API request to Ops Manager. Note that the paths don't need to be prefixed with
        '/api../v1.0' as the method does it internally"""
        headers = {"Content-Type": "application/json"}
        auth = build_auth(self.context.user, self.context.public_key)

        endpoint = f"{self.context.base_url}/api/public/v1.0{path}"
        response = requests.request(
            method, endpoint, auth=auth, headers=headers, json=json_object
        )

        if response.status_code >= 300:
            raise Exception(
                "Error sending request to Ops Manager API. {} ({}).\n Request details: {} {} (data: {})".format(
                    response.status_code, response.text, method, endpoint, json_object
                )
            )

        return response

    def find_group_id(self):
        """
        Obtains the group id of the group with specified name.
        Note, that the logic used repeats the logic used by the Operator.
        """
        if self.context.org_id is None or self.context.org_id == "":
            # If no organization is passed, then look for all organizations
            self.context.org_id = self.api_get_organization_id(self.context.group_name)
            if self.context.org_id == "":
                raise Exception(
                    f"Organization with name {self.context.group_name} not found!"
                )

        group_id = self.api_get_group_in_organization(
            self.context.org_id, self.context.group_name
        )
        if group_id == "":
            raise Exception(
                f"Group with name {self.context.group_name} not found in organization {self.context.org_id}!"
            )
        return group_id

    def api_get_organization_id(self, org_name: str) -> str:
        json = self.om_request("get", f"/orgs?name={org_name}").json()
        if len(json["results"]) > 1:
            raise Exception(
                f"More than one organizations with name {org_name} not found!"
            )
        if len(json["results"]) == 0:
            return ""
        return json["results"][0]["id"]

    def api_get_group_in_organization(self, org_id: str, group_name: str) -> str:
        json = self.om_request("get", f"/orgs/{org_id}/groups?name={group_name}").json()
        if len(json["results"]) == 0:
            return ""
        if len(json["results"]) > 1:
            raise Exception(f"More than one groups with name {group_name} found!")
        return json["results"][0]["id"]

    def api_get_hosts(self) -> Dict:
        return self.om_request("get", f"/groups/{self.context.group_id}/hosts").json()

    def get_automation_config_tester(self) -> AutomationConfigTester:
        json = self.om_request(
            "get", f"/groups/{self.context.group_id}/automationConfig"
        ).json()
        return AutomationConfigTester(json)


class OMBackgroundTester(BackgroundHealthChecker):
    """Note, that it may return sporadic 500 when the appdb is being restarted, we won't fail because of this so checking
    only for 'allowed_sequental_failures' failures. In practice having 'allowed_sequental_failures' should work as
     failures are very rare (1-2 per appdb upgrade) but let's be safe to avoid e2e flakiness."""

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
        cert_names += [
            f"{mdb_resource}-{i}-clusterfile.{namespace}" for i in range(members)
        ]

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
            names.append(
                "{}-{}-{}.{}".format(mdb_resource, shard_num, member, namespace)
            )
            if with_internal_auth_certs:
                # e.g. test-tls-x509-sc-0-2-clusterfile.developer14
                names.append(
                    "{}-{}-{}-clusterfile.{}".format(
                        mdb_resource, shard_num, member, namespace
                    )
                )

    for member in range(config_members):
        # e.g. test-tls-x509-sc-config-1.developer14
        names.append("{}-config-{}.{}".format(mdb_resource, member, namespace))
        if with_internal_auth_certs:
            # e.g. test-tls-x509-sc-config-1-clusterfile.developer14
            names.append(
                "{}-config-{}-clusterfile.{}".format(mdb_resource, member, namespace)
            )

    for mongos in range(num_mongos):
        # e.g.test-tls-x509-sc-mongos-1.developer14
        names.append("{}-mongos-{}.{}".format(mdb_resource, mongos, namespace))
        if with_internal_auth_certs:
            # e.g. test-tls-x509-sc-mongos-0-clusterfile.developer14
            names.append(
                "{}-mongos-{}-clusterfile.{}".format(mdb_resource, mongos, namespace)
            )

    if with_agent_certs:
        names.extend(get_agent_cert_names(namespace))

    return names

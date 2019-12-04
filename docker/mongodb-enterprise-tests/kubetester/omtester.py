import threading
from datetime import datetime
from typing import List

import pytest
import requests
import time
from kubetester.kubetester import build_auth

from .kubetester import get_env_var_or_fail


def running_cloud_manager():
    "Determines if the current test is running against Cloud Manager"
    return get_env_var_or_fail("OM_HOST") == "https://cloud-qa.mongodb.com"


skip_if_cloud_manager = pytest.mark.skipif(
    running_cloud_manager(), reason="Do not run in Cloud Manager"
)


# todo use @dataclass annotation https://www.python.org/dev/peps/pep-0557/
class OMContext(object):
    def __init__(self, base_url, group_id, group_name, user, public_key, org_id=None):
        self.base_url = base_url
        self.group_id = group_id
        self.group_name = group_name
        self.user = user
        self.public_key = public_key
        self.org_id = org_id


class OMTester(object):
    """ OMTester is designed to encapsulate communication with Ops Manager. It also provides the
    set of assertion methods helping to write tests"""

    def __init__(self, om_context: OMContext):
        self.om_context = om_context

    def assert_healthiness(self):
        self.do_assert_healthiness(self.om_context.base_url)
        # TODO we need to check the login page as well (/user) - does it render properly?

    def assert_om_instances_healthiness(self, pod_urls: str):
        """Checks each of the OM urls for healthiness. This is different from 'assert_healthiness' which makes
        a call to the service instead"""
        for pod_fqdn in pod_urls:
            self.do_assert_healthiness(pod_fqdn)

    def assert_version(self, version: str):
        """ makes the request to a random API url to get headers """
        response = self.om_request("get", "/api/public/v1.0/orgs")
        assert (
            f"versionString={version}" in response.headers["X-MongoDB-Service-Version"]
        )

    def assert_test_service(self):
        endpoint = self.om_context.base_url + "/test/utils/systemTime"
        response = requests.request("get", endpoint)
        assert response.status_code == 200

    def assert_support_page_enabled(self):
        """The method ends successfully if 'mms.helpAndSupportPage.enabled' is set to 'true'. It's 'false' by default.
            See mms SupportResource.supportLoggedOut()"""
        endpoint = self.om_context.base_url + "/support"
        response = requests.request("get", endpoint, allow_redirects=False)

        # logic: if mms.helpAndSupportPage.enabled==true - then status is 307, otherwise 303"
        assert response.status_code == 307

    def assert_group_exists(self):
        path = "/api/public/v1.0/groups/" + self.om_context.group_id
        response = self.om_request("get", path)

        assert response.status_code == 200

    @staticmethod
    def do_assert_healthiness(base_url: str):
        endpoint = base_url + "/monitor/health"
        response = requests.request("get", endpoint)
        assert (
            response.status_code == 200
        ), "Expected HTTP 200 from Ops Manager but got {} ({})".format(
            response.status_code, datetime.now()
        )

    def om_request(self, method, path, json_object=None):
        headers = {"Content-Type": "application/json"}
        auth = build_auth(self.om_context.user, self.om_context.public_key)

        endpoint = self.om_context.base_url + path
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


class OMBackgroundTester(threading.Thread):
    """OMBackgroundTester is the thread which periodically checks healthiness of an Ops Manager instance. It's
    run as a daemon so usually there's no need in stopping it manually.
    Note, that it may return sporadic 500 when the appdb is being restarted, we won't fail because of this so checking
    only for 'allowed_sequental_failures' failures. In practice having 'allowed_sequental_failures' should work as
     failures are very rare (1-2 per appdb upgrade) but let's be safe to avoid e2e flakiness. """

    def __init__(
        self,
        om_tester: OMTester,
        wait_sec: int = 3,
        allowed_sequental_failures: int = 3,
    ):
        super().__init__()
        self._stop_event = threading.Event()
        self.om_tester = om_tester
        self.wait_sec = wait_sec
        self.allowed_sequental_failures = allowed_sequental_failures
        self.exception_number = 0
        self.last_exception = None
        self.daemon = True
        self.max_consecutive_failure = 0

    def run(self):
        consecutive_failure = 0
        while not self._stop_event.isSet():
            try:
                self.om_tester.assert_healthiness()
                consecutive_failure = 0
            except BaseException as e:
                print(e)
                self.last_exception = e
                consecutive_failure = consecutive_failure + 1
                self.max_consecutive_failure = max(
                    self.max_consecutive_failure, consecutive_failure
                )
                self.exception_number = self.exception_number + 1
            time.sleep(self.wait_sec)

    def stop(self):
        self._stop_event.set()

    def assert_healthiness(self):
        print("\nlongest consecutive failures: {}".format(self.max_consecutive_failure))
        print("total exceptions count: {}".format(self.exception_number))
        assert self.max_consecutive_failure <= self.allowed_sequental_failures


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

import time
from typing import List

from kubetester.ldap import create_ldap_user, OpenLDAP, LDAPUser
from kubetester.helm import helm_install_from_chart, helm_uninstall
from kubetester.kubetester import KubernetesTester

from pytest import fixture
from kubernetes import client


LDAP_DUMMY_PASSWORD = "DummyPassword."
LDAP_NAME = "openldap"
LDAP_POD_LABEL = "app=openldap"
LDAP_PORT = 389
LDAP_PROTO_PLAIN = "ldap"


@fixture(scope="module")
def openldap(namespace: str) -> OpenLDAP:
    """Installs a OpenLDAP server and returns its Pod."""
    helm_install_from_chart(namespace, LDAP_NAME, "stable/openldap", "1.2.4")

    pod = get_ldap_pod_when_ready(namespace)

    yield OpenLDAP(ldap_host(namespace,), ldap_admin_password(namespace))

    helm_uninstall(LDAP_NAME)


def get_ldap_pod_when_ready(namespace: str) -> client.V1Pod:
    """Waits until the openldap Pod meets the conditions type = Ready and status = True.
    When this happens the LDAP server has already started and ready to work.
    """
    while True:
        try:
            pod = KubernetesTester.read_pod_labels(namespace, LDAP_POD_LABEL).items[0]
            for condition in pod.status.conditions:
                if condition.type == "Ready" and condition.status == "True":
                    return pod
        except client.rest.ApiException as e:
            # The Pod might not exist in Kubernetes yet so skip any 404
            if e.status != 404:
                raise

        time.sleep(3)


@fixture(scope="module")
def ldap_mongodb_users(openldap: OpenLDAP) -> List[LDAPUser]:
    user_list = [LDAPUser("mdb0", LDAP_DUMMY_PASSWORD)]
    for user in user_list:
        create_user(openldap, user)

    return user_list


def create_user(openldap: OpenLDAP, user: LDAPUser):
    create_ldap_user(openldap, user)


def ldap_host(
    namespace: str, proto: str = LDAP_PROTO_PLAIN, port: int = LDAP_PORT
) -> str:
    return "{}://{}.{}.svc.cluster.local:{}".format(proto, LDAP_NAME, namespace, port)


def ldap_admin_password(namespace: str) -> str:
    return KubernetesTester.read_secret(namespace, LDAP_NAME)["LDAP_ADMIN_PASSWORD"]

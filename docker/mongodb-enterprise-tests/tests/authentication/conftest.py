import time
from typing import List

from kubetester.ldap import create_user, OpenLDAP, LDAPUser
from kubetester.helm import helm_install_from_chart, helm_uninstall
from kubetester.kubetester import KubernetesTester
from kubetester.certs import generate_cert

from pytest import fixture
from kubernetes import client


LDAP_DUMMY_PASSWORD = "DummyPassword."
LDAP_NAME = "openldap"
LDAP_POD_LABEL = "app=openldap"
LDAP_PORT_PLAIN = 389
LDAP_PORT_TLS = 636
LDAP_PROTO_PLAIN = "ldap"
LDAP_PROTO_TLS = "ldaps"


@fixture(scope="module")
def openldap(namespace: str) -> OpenLDAP:
    """Installs a OpenLDAP server and returns a reference to it."""
    helm_install_from_chart(namespace, LDAP_NAME, "stable/openldap", "1.2.4")

    pod = get_ldap_pod_when_ready(namespace)

    yield OpenLDAP(ldap_url(namespace), ldap_admin_password(namespace))

    helm_uninstall(LDAP_NAME)


@fixture(scope="module")
def openldap_cert(namespace: str, issuer: str) -> str:
    """Returns a new secret to be used to enable TLS on LDAP."""
    host = ldap_host(namespace)
    return generate_cert(namespace, "openldap", host, issuer)


@fixture(scope="module")
def openldap_tls(namespace: str, openldap_cert: str) -> OpenLDAP:
    """Installs an OpenLDAP server with TLS configured and returns a reference to it."""
    helm_args = {
        "tls.enabled": "true",
        "tls.secret": openldap_cert,
        # Do not require client certificates
        "env.LDAP_TLS_VERIFY_CLIENT": "never",
    }
    helm_install_from_chart(
        namespace, LDAP_NAME, "stable/openldap", "1.2.4", helm_args=helm_args
    )

    pod = get_ldap_pod_when_ready(namespace)

    yield OpenLDAP(
        ldap_url(namespace, LDAP_PROTO_TLS, LDAP_PORT_TLS),
        ldap_admin_password(namespace),
    )

    helm_uninstall(LDAP_NAME)


def get_ldap_pod_when_ready(namespace: str) -> client.V1Pod:
    """Waits until the openldap Pod meets the conditions type = Ready and status = True.
    When this happens the LDAP server has already started and ready to work.
    """
    while True:
        time.sleep(3)

        try:
            pods = KubernetesTester.read_pod_labels(namespace, LDAP_POD_LABEL)
            if pods is None:
                continue

            pod = pods.items[0]
            for condition in pod.status.conditions:
                if condition.type == "Ready" and condition.status == "True":
                    return pod

        except client.rest.ApiException as e:
            # The Pod might not exist in Kubernetes yet so skip any 404
            if e.status != 404:
                raise


@fixture(scope="module")
def ldap_mongodb_users_tls(openldap_tls: OpenLDAP, ca_path: str) -> List[LDAPUser]:
    user_list = [LDAPUser("mdb0", LDAP_DUMMY_PASSWORD)]
    for user in user_list:
        create_user(openldap_tls, user, ca_path=ca_path)

    return user_list


@fixture(scope="module")
def ldap_mongodb_users(openldap: OpenLDAP) -> List[LDAPUser]:
    user_list = [LDAPUser("mdb0", LDAP_DUMMY_PASSWORD)]
    for user in user_list:
        create_user(openldap, user)

    return user_list


def ldap_host(namespace: str) -> str:
    return "{}.{}.svc.cluster.local".format(LDAP_NAME, namespace)


def ldap_url(
    namespace: str, proto: str = LDAP_PROTO_PLAIN, port: int = LDAP_PORT_PLAIN
) -> str:
    host = ldap_host(namespace)
    return "{}://{}:{}".format(proto, host, port)


def ldap_admin_password(namespace: str) -> str:
    return KubernetesTester.read_secret(namespace, LDAP_NAME)["LDAP_ADMIN_PASSWORD"]

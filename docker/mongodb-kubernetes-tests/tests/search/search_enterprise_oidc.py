import base64
import functools
import json
import os

import kubetester.oidc as oidc
import requests
import yaml
from kubetester import create_or_update_configmap, create_or_update_secret, try_load
from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fail, fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator, get_issuer_ca_filepath
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

# Name-linked resources: the MongoDBSearch source is resolved from the
# MongoDB resource with the same name.
MDB_RESOURCE_NAME = "mdb-ent-oidc"
MDBS_RESOURCE_NAME = "mdb-ent-oidc"

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

# configurationName of the OIDC provider on the source MongoDB; the mongot
# user's OIDC username is "<configurationName>/<token sub>" (see oidc_mongot_user).
CONFIGURATION_NAME = "OIDC-test-user"

# Secret holding the Cognito app-client secret (spec.source.oidc.clientSecretSecretRef)
COGNITO_CLIENT_SECRET_NAME = f"{MDB_RESOURCE_NAME}-cognito-client-secret"

# ConfigMap holding the IdP CA that validates the Cognito token endpoint's TLS
# certificate (spec.source.oidc.idpCAConfigMapRef) -- Amazon Root CA 1.
COGNITO_IDP_CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-cognito-idp-ca"

# Requires the Cognito test environment (the cognito_* expansions). The tests
# are collected and skipped (not deselected) when it is not configured, so the
# Evergreen launcher's `pytest -m e2e_search_enterprise_oidc` run exits 0.
_COGNITO_CONFIGURED = bool(
    oidc.get_cognito_workload_client_id()
    and oidc.get_cognito_workload_url()
    and oidc.get_cognito_workload_user_id()
    and os.getenv("cognito_workload_federation_client_secret")
    and oidc.get_cognito_workload_token_endpoint()
)
_TOKEN_ENDPOINT_IS_HTTPS = oidc.get_cognito_workload_token_endpoint().startswith("https://")

pytestmark = mark.skipif(
    not (_COGNITO_CONFIGURED and _TOKEN_ENDPOINT_IS_HTTPS),
    reason="Cognito OIDC test environment is not configured (cognito_* expansions missing or token endpoint not HTTPS)",
)


def _decode_jwt_payload(token: str) -> dict:
    """Decodes the JWT payload segment without signature verification."""
    payload_segment = token.split(".")[1]
    # restore the base64url padding stripped per RFC 7515
    payload_segment += "=" * (-len(payload_segment) % 4)
    return json.loads(base64.urlsafe_b64decode(payload_segment))


@functools.lru_cache(maxsize=None)
def _client_credentials_claims() -> dict:
    """Fetches a client-credentials token from the Cognito token endpoint and
    returns its decoded payload.

    mongot authenticates with a client-credentials token, whose aud and sub
    claims can differ from the user-login contract used by the m2m fixtures (those
    authenticate with the Cognito ID token, which carries aud): a Cognito
    client-credentials token has no aud unless the request asks for a resource
    binding, and its sub identifies the app client rather than the workload user.
    The OIDC provider audience and the $external username below are derived from
    this token, so the source of truth is the IdP, not the user-flow fixtures.
    """
    token_endpoint = oidc.get_cognito_workload_token_endpoint()
    response = requests.post(
        token_endpoint,
        data={
            "grant_type": "client_credentials",
            "client_id": oidc.get_cognito_workload_client_id(),
            "client_secret": os.getenv("cognito_workload_federation_client_secret"),
        },
        timeout=30,
    )
    if not 200 <= response.status_code < 300:
        fail(
            f"Client-credentials token request to {token_endpoint} failed with HTTP "
            f"{response.status_code}: {response.text}. The Cognito app-client/token-endpoint "
            "setup for this test is broken.",
            pytrace=False,
        )
    access_token = response.json().get("access_token")
    if not access_token:
        fail(
            f"Token endpoint {token_endpoint} returned no access_token: {response.text}. "
            "The Cognito app-client/token-endpoint setup for this test is broken.",
            pytrace=False,
        )
    return _decode_jwt_payload(access_token)


def _cc_token_audience() -> str:
    """Audience of the token the app client actually mints (for oidcProviderConfigs)."""
    claims = _client_credentials_claims()
    # mongod validates the configured audience against the token's aud claim only — the
    # server's JWT parser drops client_id/azp, so an aud-less token cannot authenticate.
    # Cognito access tokens carry aud only when the token request asks for a resource
    # binding, so fail clearly here instead of surfacing as a mongot auth failure.
    audience = claims.get("aud")
    if isinstance(audience, list):
        if len(audience) != 1:
            fail(
                f"The Cognito client-credentials token has a multi-valued aud {audience}; "
                "mongod accepts exactly one audience.",
                pytrace=False,
            )
        audience = audience[0]
    if not audience:
        fail(
            "The Cognito client-credentials token carries no aud claim; mongod validates "
            "audience against aud only. The Cognito pool must mint audience-bearing access "
            "tokens (a resource binding on the token request) for this test to work.",
            pytrace=False,
        )
    return audience


def _cc_token_subject() -> str:
    """Subject of the token the app client actually mints (for the $external username)."""
    subject = _client_credentials_claims().get("sub")
    if not subject:
        fail(
            "The Cognito client-credentials token carries no sub claim; cannot derive the "
            "$external OIDC username for mongot.",
            pytrace=False,
        )
    return subject


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource.configure(om=get_ops_manager(namespace), project_name=MDB_RESOURCE_NAME)
    # TLS must be enabled on the sync source for OIDC auth (mongot requires a CA
    # to trust the source's TLS certificate).
    resource.configure_custom_tls(issuer_ca_configmap, "certs")
    resource["spec"]["security"]["authentication"] = {
        "agents": {"mode": "SCRAM"},
        "enabled": True,
        "modes": ["SCRAM", "OIDC"],
        "oidcProviderConfigs": [
            {
                # audience is derived from the client-credentials token the app
                # client actually mints (see _cc_token_audience), so mongod's
                # audience validation matches the tokens mongot presents.
                "audience": _cc_token_audience(),
                "clientId": oidc.get_cognito_workload_client_id(),
                "issuerURI": oidc.get_cognito_workload_url(),
                "requestedScopes": [],
                "userClaim": "sub",
                "authorizationMethod": "WorkloadIdentityFederation",
                "authorizationType": "UserID",
                "configurationName": CONFIGURATION_NAME,
            }
        ],
    }
    try_load(resource)
    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"), namespace=namespace, name=MDBS_RESOURCE_NAME
    )
    if "spec" not in resource:
        resource["spec"] = {}

    # OIDC client-credentials auth for the sync source (replaces username/passwordSecretRef)
    resource["spec"]["source"] = {
        "oidc": {
            "tokenEndpoint": oidc.get_cognito_workload_token_endpoint(),
            "clientId": oidc.get_cognito_workload_client_id(),
            "clientSecretSecretRef": {"name": COGNITO_CLIENT_SECRET_NAME, "key": "clientSecret"},
            "idpCAConfigMapRef": {"name": COGNITO_IDP_CA_CONFIGMAP_NAME},
        },
    }

    try_load(resource)
    return resource


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(f"{MDB_RESOURCE_NAME}-{ADMIN_USER_NAME}")


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(f"{MDB_RESOURCE_NAME}-{USER_NAME}")


@fixture(scope="function")
def oidc_mongot_user(namespace: str, helper: SearchDeploymentHelper) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
        namespace=namespace,
        name=f"{MDB_RESOURCE_NAME}-mongot-oidc-user",
    )
    if try_load(resource):
        return resource
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    # the $external principal matches the sub of the client-credentials token the
    # app client actually mints (see _cc_token_subject), not the workload user id
    resource["spec"]["username"] = f"{CONFIGURATION_NAME}/{_cc_token_subject()}"
    resource["spec"]["db"] = "$external"
    # OIDC users don't use password auth -- remove passwordSecretKeyRef if present
    resource["spec"].pop("passwordSecretKeyRef", None)
    return resource


@mark.e2e_search_enterprise_oidc
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_enterprise_oidc
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_oidc
def test_install_tls_secrets_and_oidc_configmaps(namespace: str, mdb: MongoDB, issuer: str, amazon_ca_1_filepath: str):
    # MongoDB server certs (TLS enabled on the sync source is required for OIDC auth)
    create_mongodb_tls_certs(issuer, namespace, mdb.name, f"certs-{mdb.name}-cert", mdb.get_members())

    # OIDC client secret for mongot's client-credentials grant at the Cognito token endpoint
    # (non-empty is guaranteed by the module-level Cognito guard)
    client_secret = os.getenv("cognito_workload_federation_client_secret")
    assert client_secret
    create_or_update_secret(
        namespace,
        name=COGNITO_CLIENT_SECRET_NAME,
        data={"clientSecret": client_secret},
    )

    # IdP CA (Amazon Root CA 1) validating the Cognito token endpoint's TLS certificate
    with open(amazon_ca_1_filepath) as f:
        create_or_update_configmap(namespace, name=COGNITO_IDP_CA_CONFIGMAP_NAME, data={"ca.crt": f.read()})


@mark.e2e_search_enterprise_oidc
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_oidc
def test_create_users(
    helper: SearchDeploymentHelper,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    oidc_mongot_user: MongoDBUser,
):
    # Admin user (password auth)
    helper.apply_user_password(admin_user, ADMIN_USER_PASSWORD)
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

    # Regular user (password auth)
    helper.apply_user_password(user, USER_PASSWORD)
    user.assert_reaches_phase(Phase.Updated, timeout=300)

    # OIDC mongot user (no password needed)
    oidc_mongot_user.update()
    oidc_mongot_user.assert_reaches_phase(Phase.Updated, timeout=300)


@mark.e2e_search_enterprise_oidc
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_oidc
def test_wait_for_database_resource_ready(mdb: MongoDB):
    # The mongot wiring is applied as an in-memory automation-config override and does not bump
    # the MongoDB generation, so there is no phase/generation transition to wait on; poll the
    # mongods for the search parameters directly.
    def mongot_params_wired(m: MongoDB) -> bool:
        if m.get_status_phase() != Phase.Running:
            return False
        for idx in range(m.get_members()):
            mongod_config = yaml.safe_load(
                KubernetesTester.run_command_in_pod_container(
                    f"{m.name}-{idx}", m.namespace, ["cat", "/data/automation-mongod.conf"]
                )
            )
            set_parameter = mongod_config.get("setParameter", {})
            if "mongotHost" not in set_parameter or "searchIndexManagementHostAndPort" not in set_parameter:
                return False
        return True

    mdb.wait_for(mongot_params_wired, timeout=600, should_raise=True)


@fixture(scope="function")
def sample_movies_helper(mdb: MongoDB, namespace: str) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(
            mdb, f"{MDB_RESOURCE_NAME}-{USER_NAME}", USER_PASSWORD, use_ssl=True, ca_path=get_issuer_ca_filepath()
        ),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace, mdb.get_version()),
    )


@mark.e2e_search_enterprise_oidc
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_enterprise_oidc
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_enterprise_oidc
def test_search_assert_search_query(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=60)

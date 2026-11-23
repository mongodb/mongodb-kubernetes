import base64
import functools
import json
from urllib.parse import urlparse

import kubetester.oidc as oidc
import requests
from kubetester import create_or_update_configmap, create_or_update_secret, try_load
from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fail, fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.replicaset_search_helper import verify_rs_mongod_parameters
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

# Secret holding the OIDC app-client secret (spec.source.oidc.clientSecretSecretRef)
OIDC_CLIENT_SECRET_NAME = f"{MDB_RESOURCE_NAME}-oidc-client-secret"

# ConfigMap holding the IdP CA that validates the OIDC token endpoint's TLS
# certificate (spec.source.oidc.idpCAConfigMapRef) -- Amazon Root CA 1 for Cognito,
# ISRG Root X1 for Auth0.
OIDC_IDP_CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-oidc-idp-ca"

# The OIDC sync-source block needs the enterprise mongot build at or above the operator's
# 1.78.0 OIDC floor; the chart default is community content that cannot parse it. This is the
# internal per-merge enterprise build; the GA quay image replaces it once promoted.
MONGOT_ENTERPRISE_IMAGE_REPO = "901841024863.dkr.ecr.us-east-1.amazonaws.com"
MONGOT_ENTERPRISE_IMAGE_NAME = "mongot-community/rapid-releases"
MONGOT_ENTERPRISE_IMAGE_TAG = "1.79.0-88-gbfa7e50bd4-ent"

# Requires the OIDC test environment (the cognito_* or auth0_* expansions). Tests fail when
# it is not configured rather than silently skipping: the expansions come from project
# variables, so a miss is a misconfiguration that should read red, not green.
_OIDC_PROVIDER_CONFIGURED = oidc.oidc_test_provider_configured()


@fixture(autouse=True)
def _require_oidc_provider_env():
    if not _OIDC_PROVIDER_CONFIGURED:
        fail(
            "OIDC test provider is not configured (oidc_provider=%s client credentials missing)"
            % oidc.get_oidc_provider(),
            pytrace=False,
        )


def _decode_jwt_payload(token: str) -> dict:
    """Decodes the JWT payload segment without signature verification."""
    payload_segment = token.split(".")[1]
    # restore the base64url padding stripped per RFC 7515
    payload_segment += "=" * (-len(payload_segment) % 4)
    return json.loads(base64.urlsafe_b64decode(payload_segment))


@functools.lru_cache(maxsize=None)
def _client_credentials_claims() -> dict:
    """Fetches a client-credentials token from the configured OIDC provider's token
    endpoint and returns its decoded payload.

    mongot authenticates with a client-credentials token, whose aud and sub
    claims can differ from the user-login contract used by the m2m fixtures (those
    authenticate with an ID token, which carries aud). Whether a client-credentials
    token carries aud depends on the provider: Auth0 mints one only when the request
    sends audience, and Cognito needs a pool-side pre-token Lambda to add it. The OIDC
    provider audience and the $external username below are derived from this token, so
    the source of truth is the IdP, not the user-flow fixtures.

    Every failure path here is a fail(..., pytrace=False), which prints the message only:
    pytest runs with -l, so an exception escaping this frame would print its locals, and
    they hold the client secret and the access token.
    """
    token_endpoint = oidc.get_oidc_token_endpoint()
    provider = oidc.get_oidc_provider()
    endpoint_host = urlparse(token_endpoint).netloc
    # A non-HTTPS endpoint is a misconfiguration, not a reason to skip: fail before the
    # token request sends the client secret over plain HTTP.
    if not token_endpoint.startswith("https://"):
        fail(
            f"Derived token endpoint is not HTTPS: {token_endpoint}. "
            f"Check the {provider} issuer/discovery configuration.",
            pytrace=False,
        )
    # Auth0 requires an explicit audience; Cognito and Entra/Okta derive it server-side.
    audience = oidc.get_oidc_audience()
    try:
        response = oidc.oidc_session().post(
            token_endpoint,
            data={
                "grant_type": "client_credentials",
                "client_id": oidc.get_oidc_client_id(),
                "client_secret": oidc.get_oidc_client_secret(),
                **({"audience": audience} if audience else {}),
            },
            timeout=30,
        )
    except requests.RequestException as e:
        fail(
            f"Could not reach the {provider} token endpoint at {endpoint_host} ({type(e).__name__}).",
            pytrace=False,
        )
    if not 200 <= response.status_code < 300:
        fail(
            f"The {provider} token endpoint at {endpoint_host} returned HTTP "
            f"{response.status_code}. The app-client/token-endpoint setup for this test is broken.",
            pytrace=False,
        )
    # The catch is broad on purpose: a 2xx body can still be a JSON array, an access_token
    # that is null, or a string that is not a JWT, and this function turns any of them into
    # one clear message rather than a traceback.
    try:
        return _decode_jwt_payload(response.json()["access_token"])
    except (AttributeError, IndexError, KeyError, TypeError, ValueError):
        fail(
            f"The {provider} token endpoint at {endpoint_host} returned no usable "
            "access_token. The app-client/token-endpoint setup for this test is broken.",
            pytrace=False,
        )


def _cc_token_audience() -> str:
    """Audience of the token the app client actually mints (for oidcProviderConfigs); see
    _client_credentials_claims for why it comes from the client-credentials token."""
    claims = _client_credentials_claims()
    audience = claims.get("aud")
    if isinstance(audience, list):
        if len(audience) != 1:
            fail(
                f"The {oidc.get_oidc_provider()} client-credentials token has a multi-valued "
                f"aud {audience}; mongod accepts exactly one audience.",
                pytrace=False,
            )
        audience = audience[0]
    if not audience:
        # Only Cognito needs a pool-side mechanism to mint aud: Auth0 emits it when the
        # request carries audience, which _client_credentials_claims already sends.
        hint = ""
        if oidc.get_oidc_provider() == "cognito":
            hint = (
                " The Cognito pool needs a V3_0 pre-token-generation Lambda that adds aud "
                "(the app client id) to access tokens for this test to work."
            )
        fail(
            f"The {oidc.get_oidc_provider()} client-credentials token carries no aud claim; "
            f"mongod validates audience against aud only.{hint}",
            pytrace=False,
        )
    return audience


@fixture(scope="session")
def cc_claims() -> dict:
    """The client-credentials token's claims, fetched once per session. A provider outage
    surfaces here with a clear message instead of as a fixture error on the first test
    to request mdb."""
    try:
        return _client_credentials_claims()
    except requests.RequestException as e:
        fail(
            f"Could not fetch a client-credentials token from {oidc.get_oidc_provider()}: {e}",
            pytrace=False,
        )


def _cc_token_subject() -> str:
    """Subject of the token the app client actually mints (for the $external username); see
    _client_credentials_claims."""
    subject = _client_credentials_claims().get("sub")
    if not subject:
        fail(
            f"The {oidc.get_oidc_provider()} client-credentials token carries no sub claim; "
            "cannot derive the $external OIDC username for mongot.",
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


@fixture(scope="module")
def mdb(namespace: str, issuer_ca_configmap: str, cc_claims: dict) -> MongoDB:
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
                # audience must match the token the app client mints (see _client_credentials_claims).
                "audience": _cc_token_audience(),
                "clientId": oidc.get_oidc_client_id(),
                "issuerURI": oidc.get_oidc_issuer(),
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
    oidc_source = {
        "tokenEndpoint": oidc.get_oidc_token_endpoint(),
        "clientId": oidc.get_oidc_client_id(),
        "clientSecretSecretRef": {"name": OIDC_CLIENT_SECRET_NAME, "key": "clientSecret"},
        "idpCAConfigMapRef": {"name": OIDC_IDP_CA_CONFIGMAP_NAME},
    }
    # Auth0 requires an explicit audience on spec.source.oidc; omit the key when empty.
    audience = oidc.get_oidc_audience()
    if audience:
        oidc_source["audience"] = audience
    resource["spec"]["source"] = {"oidc": oidc_source}

    # The OIDC sync-source block needs the enterprise mongot build (the chart default is
    # community content that cannot parse it) at or above the 1.78.0 OIDC floor.
    resource["spec"]["version"] = MONGOT_ENTERPRISE_IMAGE_TAG

    try_load(resource)
    return resource


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(f"{MDB_RESOURCE_NAME}-{ADMIN_USER_NAME}")


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(f"{MDB_RESOURCE_NAME}-{USER_NAME}")


@fixture(scope="function")
def oidc_mongot_user(namespace: str, helper: SearchDeploymentHelper, cc_claims: dict) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
        namespace=namespace,
        name=f"{MDB_RESOURCE_NAME}-mongot-oidc-user",
    )
    if try_load(resource):
        return resource
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    # the $external principal matches the minted token's sub (see _client_credentials_claims).
    resource["spec"]["username"] = f"{CONFIGURATION_NAME}/{_cc_token_subject()}"
    resource["spec"]["db"] = "$external"
    # OIDC users don't use password auth -- remove passwordSecretKeyRef if present
    resource["spec"].pop("passwordSecretKeyRef", None)
    return resource


# Pins the enterprise mongot image for the whole namespace: no other module may share it.
@fixture(scope="module")
def oidc_operator_installation_config(operator_installation_config: dict[str, str]) -> dict[str, str]:
    config = dict(operator_installation_config)
    config["search.repo"] = MONGOT_ENTERPRISE_IMAGE_REPO
    config["search.name"] = MONGOT_ENTERPRISE_IMAGE_NAME
    config["search.version"] = MONGOT_ENTERPRISE_IMAGE_TAG
    return config


@mark.e2e_search_enterprise_oidc
def test_install_operator(namespace: str, oidc_operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=oidc_operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_enterprise_oidc
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@fixture(scope="module")
def idp_ca_filepath() -> str:
    """CA bundle that validates the configured provider's token endpoint TLS certificate:
    Amazon Root CA 1 for Cognito, ISRG Root X1 (Let's Encrypt) for Auth0."""
    name = "letsencrypt-isrg-root-x1.pem" if oidc.get_oidc_provider() == "auth0" else "amazon-ca-1.pem"
    return yaml_fixture(name)


@mark.e2e_search_enterprise_oidc
def test_install_tls_secrets_and_oidc_configmaps(namespace: str, mdb: MongoDB, issuer: str, idp_ca_filepath: str):
    # MongoDB server certs (TLS enabled on the sync source is required for OIDC auth)
    create_mongodb_tls_certs(issuer, namespace, mdb.name, f"certs-{mdb.name}-cert", mdb.get_members())

    # OIDC client secret for mongot's client-credentials grant at the token endpoint
    # (non-empty is guaranteed by the module-level OIDC provider guard)
    create_or_update_secret(
        namespace,
        name=OIDC_CLIENT_SECRET_NAME,
        data={"clientSecret": oidc.get_oidc_client_secret()},
    )

    # IdP CA validating the token endpoint's TLS certificate (Amazon Root CA 1 for
    # Cognito, ISRG Root X1 for Auth0)
    with open(idp_ca_filepath) as f:
        create_or_update_configmap(namespace, name=OIDC_IDP_CA_CONFIGMAP_NAME, data={"ca.crt": f.read()})


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

    # Regular user (password auth)
    helper.apply_user_password(user, USER_PASSWORD)

    # OIDC mongot user (no password needed)
    oidc_mongot_user.update()

    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)
    user.assert_reaches_phase(Phase.Updated, timeout=300)
    oidc_mongot_user.assert_reaches_phase(Phase.Updated, timeout=300)


@mark.e2e_search_enterprise_oidc
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_oidc
def test_wait_for_database_resource_ready(mdb: MongoDB):
    expected_host = search_resource_names.mongot_pod_fqdn(MDBS_RESOURCE_NAME, mdb.namespace, 27028)
    verify_rs_mongod_parameters(mdb.namespace, mdb.name, mdb.get_members(), expected_host)


@fixture(scope="module")
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

import functools
import os

import requests
from kubetester.http import get_retriable_https_session

# Note: The project uses AWS Cognito in the mongodb-mms-testing AWS account to facilitate OIDC authentication testing.
# This setup includes:

# User Pool: A user pool in Cognito manages the identities.
# Users: We use the user credentials to do authentication.
# App Client: An app client is configured for machine-to-machine (M2M) authentication.
# Groups: Cognito groups are used to manage users from the user pool for GroupMembership access.

# Environment variables and secrets required for these tests (like client IDs, URLs, and user IDs, as seen in the Python code)
# are stored in Evergreen and fetched from there during test execution.

# A session explaining the setup can be found here: http://go/k8s-oidc-session


def get_cognito_workload_client_id() -> str:
    return os.getenv("cognito_workload_federation_client_id", "")


def get_cognito_workload_url() -> str:
    return os.getenv("cognito_workload_url", "")


def get_cognito_workload_user_id() -> str:
    return os.getenv("cognito_workload_user_id", "")


def get_oidc_provider() -> str:
    return os.getenv("oidc_provider", "cognito")


def get_oidc_issuer() -> str:
    """Issuer URL of the configured OIDC test provider."""
    if get_oidc_provider() == "auth0":
        return os.getenv("auth0_issuer", "")
    return get_cognito_workload_url()


def get_oidc_client_id() -> str:
    if get_oidc_provider() == "auth0":
        return os.getenv("auth0_client_id", "")
    return get_cognito_workload_client_id()


def get_oidc_client_secret() -> str:
    if get_oidc_provider() == "auth0":
        return os.getenv("auth0_client_secret", "")
    return os.getenv("cognito_workload_federation_client_secret", "")


def get_oidc_audience() -> str:
    """Audience requested at the token endpoint. Empty when the provider does not need one:
    Cognito adds aud with a pool-side pre-token Lambda, and Entra/Okta derive it server-side."""
    if get_oidc_provider() == "auth0":
        return os.getenv("auth0_audience", "")
    return ""


# Retryable IdP responses: the token endpoint rate-limits (429) and the provider's edge
# returns 5xx under load.
_TRANSIENT_STATUSES = (429, 500, 502, 503, 504)


@functools.lru_cache(maxsize=None)
def oidc_session() -> requests.Session:
    """IdP calls use the shared retry policy: allowed_methods=None retries the token POST too,
    raise_on_status=False returns a 4xx for the caller to inspect."""
    return get_retriable_https_session(
        tls_verify=True,
        status_forcelist=_TRANSIENT_STATUSES,
        allowed_methods=None,
        respect_retry_after_header=False,
    )


@functools.lru_cache(maxsize=None)
def _discovery_document(issuer: str) -> dict:
    """The provider's OIDC discovery document, fetched once per issuer: the token endpoint
    is read from it by the token fetch and the mdbs fixture alike."""
    discovery_url = f"{issuer.rstrip('/')}/.well-known/openid-configuration"
    response = oidc_session().get(discovery_url, timeout=30)
    response.raise_for_status()
    return response.json()


def get_oidc_token_endpoint() -> str:
    """The provider's token endpoint, derived from its OIDC discovery document."""
    return _discovery_document(get_oidc_issuer())["token_endpoint"]


def oidc_test_provider_configured() -> bool:
    """True when the selected provider's client credentials are present."""
    return bool(get_oidc_client_id() and get_oidc_issuer() and get_oidc_client_secret())

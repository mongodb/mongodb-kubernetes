import base64
import functools
import json
import os

import kubetester.oidc as oidc
import pytest
import requests
from pytest import fail, mark

# Probes the Cognito user pool the e2e harness already uses (kubetester/oidc.py, setup
# session go/k8s-oidc-session) with a machine-to-machine client-credentials grant, and
# inspects the access token claims mongod validates against. It runs inside the e2e
# harness like the other OIDC tests but deploys nothing: it needs only the three
# existing cognito_* expansions and outbound HTTPS to the pool.
#
# Why an aud assertion matters here, with the documentation behind each claim:
#
# - A Cognito M2M access token carries no `aud` claim. The claim set is documented in
#   "Machine-to-machine access token payload" under
#   https://docs.aws.amazon.com/cognito/latest/developerguide/amazon-cognito-user-pools-using-the-access-token.html
#   (sub = the app client id, iss, token_use, exp, iat, jti and scopes; no aud).
# - For user-flow access tokens, `aud` appears "only if your application requested a
#   resource binding" (same page), and a resource binding cannot be requested with a
#   client-credentials grant:
#   https://docs.aws.amazon.com/cognito/latest/developerguide/cognito-user-pools-define-resource-servers.html
# - mongod requires a non-empty `audience` on every OIDC identity-provider
#   configuration and validates the token's `aud` against it, accepting exactly one
#   value (multi-aud rejection is deliberate, SERVER-86607; see the server's
#   sasl_oidc_server_conversation.cpp and idp_manager.cpp). A token without `aud`
#   therefore cannot authenticate to mongod, whatever the configuration says.
# - The existing m2m e2e tests (e2e_replica_set_oidc_m2m_user and friends) do not
#   contradict this: they authenticate a user with a password through the M2M-configured
#   app client and present the ID token (kubetester/mongotester.py, MyOIDCCallback
#   returns u.id_token), whose `aud` is the app client id - exactly the audience they
#   configure on the provider. No client-credentials grant is exercised anywhere in the
#   suite but here.
# - The workaround is pool-side: a V3_0 pre-token-generation Lambda trigger can add
#   `aud` to access tokens (the AWS doc's own example sets it to
#   event.callerContext.clientId), requiring the user pool to be on the Essentials or
#   Plus feature plan:
#   https://docs.aws.amazon.com/cognito/latest/developerguide/user-pool-lambda-pre-token-generation.html
#
# Until the pool runs that Lambda, the aud test below is xfail (strict=False): the task
# stays green, the run logs the real token's claims as evidence, and the test flips to
# XPASS when the Lambda lands.

# The Cognito test environment (the existing Evergreen cognito_* expansions used by the
# m2m tests; no new variables - the token endpoint is derived from the issuer's public
# OIDC discovery document below, the same discovery mongod relies on).
_COGNITO_CONFIGURED = bool(
    oidc.get_cognito_workload_client_id()
    and oidc.get_cognito_workload_url()
    and os.getenv("cognito_workload_federation_client_secret")
)
pytestmark = mark.skipif(
    not _COGNITO_CONFIGURED,
    reason="Cognito OIDC test environment is not configured (cognito_* expansions missing)",
)


def _decode_jwt_payload(token: str) -> dict:
    """Decodes the JWT payload segment without signature verification.

    This probe inspects claims, not provenance: mongod verifies the signature against
    the pool's JWKS in a real deployment.
    """
    payload_segment = token.split(".")[1]
    # restore the base64url padding stripped per RFC 7515
    payload_segment += "=" * (-len(payload_segment) % 4)
    return json.loads(base64.urlsafe_b64decode(payload_segment))


def _token_endpoint() -> str:
    """Derives the token endpoint from the issuer's public OIDC discovery document."""
    discovery_url = f"{oidc.get_cognito_workload_url().rstrip('/')}/.well-known/openid-configuration"
    response = requests.get(discovery_url, timeout=30)
    if not 200 <= response.status_code < 300:
        fail(
            f"OIDC discovery request to {discovery_url} failed with HTTP " f"{response.status_code}: {response.text}.",
            pytrace=False,
        )
    token_endpoint = response.json().get("token_endpoint")
    if not token_endpoint:
        fail(f"Discovery document at {discovery_url} carries no token_endpoint: {response.text}.", pytrace=False)
    return token_endpoint


@functools.lru_cache(maxsize=None)
def _cc_token_claims() -> dict:
    """Fetches a client-credentials access token from the pool and returns its decoded payload."""
    token_endpoint = _token_endpoint()
    # A non-HTTPS endpoint would send the client secret in clear; that is a
    # misconfiguration of the expansions, not a reason to skip.
    if not token_endpoint.startswith("https://"):
        fail(f"Token endpoint derived from the discovery document is not HTTPS: {token_endpoint}.", pytrace=False)
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
            f"{response.status_code}: {response.text}. The Cognito app-client setup for "
            "this test is broken.",
            pytrace=False,
        )
    access_token = response.json().get("access_token")
    if not access_token:
        fail(f"Token endpoint {token_endpoint} returned no access_token: {response.text}.", pytrace=False)
    return _decode_jwt_payload(access_token)


@mark.e2e_oidc_cognito_cc_aud
def test_cc_token_contract():
    """The M2M contract: token_use=access and sub=the app client id.

    Both claims are documented in the "Machine-to-machine access token payload"
    section of the AWS access-token page linked in the module docstring.
    """
    claims = _cc_token_claims()
    print(json.dumps(claims, indent=2, default=str))

    assert claims.get("token_use") == "access", f"token_use={claims.get('token_use')!r}, expected 'access'"
    assert claims.get("sub") == oidc.get_cognito_workload_client_id(), (
        f"sub={claims.get('sub')!r} does not match the app client id; the M2M identity "
        "contract documented by AWS has sub equal to the requesting app client id"
    )


@mark.e2e_oidc_cognito_cc_aud
@mark.xfail(
    reason="The Cognito pool has no V3_0 pre-token-generation Lambda adding aud to access "
    "tokens yet (see the module docstring); mongod cannot validate an aud-less token "
    "against its required audience. Flips to XPASS when the Lambda lands.",
    strict=False,
)
def test_cc_token_carries_single_valued_aud_for_mongod():
    """mongod requires the token's aud to match the configured audience, exactly one value.

    Without a pool-side Lambda adding it, the client-credentials token carries no aud
    (AWS docs, module docstring) and no M2M search/OIDC e2e can authenticate to mongod.
    """
    claims = _cc_token_claims()
    print(json.dumps(claims, indent=2, default=str))

    audience = claims.get("aud")
    if isinstance(audience, list):
        assert len(audience) == 1, f"Multi-valued aud {audience}; mongod accepts exactly one audience " "(SERVER-86607)"
        audience = audience[0]
    assert audience, (
        "The client-credentials access token carries no aud claim. mongod validates the "
        "configured audience against the token's aud only, so this token cannot "
        "authenticate. The pool needs a V3_0 pre-token-generation Lambda that adds aud "
        "(the app client id) to access tokens: "
        "https://docs.aws.amazon.com/cognito/latest/developerguide/user-pool-lambda-pre-token-generation.html"
    )

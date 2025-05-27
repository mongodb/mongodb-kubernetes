import os


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

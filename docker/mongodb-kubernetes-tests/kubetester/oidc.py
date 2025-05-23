import os


def get_cognito_workload_client_id() -> str:
    return os.getenv("cognito_workload_federation_client_id", "")


def get_cognito_workload_url() -> str:
    return os.getenv("cognito_workload_url", "")


def get_cognito_workload_user_id() -> str:
    return os.getenv("cognito_workload_user_id", "")

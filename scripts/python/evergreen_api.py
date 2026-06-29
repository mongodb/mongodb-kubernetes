import os

import requests
from evergreen.api import EvergreenApi, EvgAuth

EVERGREEN_API = "https://evergreen.mongodb.com/api"


def get_evergreen_auth_headers() -> dict:
    """
    Returns Evergreen API authentication headers using EVERGREEN_USER and EVERGREEN_API_KEY environment variables.
    Raises RuntimeError if either variable is missing.

    DEPRECATED: Use get_evergreen_api() instead for new code.
    """
    evg_user = os.environ.get("EVERGREEN_USER", "")
    api_key = os.environ.get("EVERGREEN_API_KEY", "")
    if evg_user == "" or api_key == "":
        raise RuntimeError("EVERGREEN_USER and EVERGREEN_API_KEY must be set")
    return {"Api-User": evg_user, "Api-Key": api_key}


def get_evergreen_api() -> EvergreenApi:
    """
    Returns an EvergreenApi client instance using EVERGREEN_USER and EVERGREEN_API_KEY environment variables.
    Raises RuntimeError if either variable is missing.
    """
    evg_user = os.environ.get("EVERGREEN_USER", "")
    api_key = os.environ.get("EVERGREEN_API_KEY", "")
    if evg_user == "" or api_key == "":
        raise RuntimeError("EVERGREEN_USER and EVERGREEN_API_KEY must be set")

    auth = EvgAuth(evg_user, api_key)
    return EvergreenApi.get_api(auth)


def get_task_details(task_id: str) -> dict:
    """
    Fetch task details from Evergreen API for a given task_id.
    Returns the JSON response as a dict.
    Raises requests.HTTPError if the request fails.
    """
    url = f"{EVERGREEN_API}/rest/v2/tasks/{task_id}"
    headers = get_evergreen_auth_headers()
    response = requests.get(url, headers=headers)
    response.raise_for_status()
    return response.json()

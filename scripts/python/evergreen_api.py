import os

import requests

EVERGREEN_API = "https://evergreen.mongodb.com/api"


def get_evergreen_auth_headers() -> dict:
    """
    Returns Evergreen API authentication headers using EVERGREEN_USER and EVERGREEN_API_KEY environment variables.
    Raises RuntimeError if either variable is missing.
    """
    evg_user = os.environ.get("EVERGREEN_USER", "")
    api_key = os.environ.get("EVERGREEN_API_KEY", "")
    if evg_user == "" or api_key == "":
        raise RuntimeError("EVERGREEN_USER and EVERGREEN_API_KEY must be set")
    return {"Api-User": evg_user, "Api-Key": api_key}


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

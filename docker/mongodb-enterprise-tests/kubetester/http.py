from typing import Tuple

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry


def get_retriable_session(proto: str, tls_verify: bool) -> requests.Session:
    """
    Returns a request Session object with a retry mechanism.

    This is required to overcome a DNS resolution problem that we have
    experienced in the Evergreen hosts. This can also probably alleviate
    problems arising from request throttling.
    """

    s = requests.Session()

    s.verify = tls_verify
    retries = Retry(
        total=5,
        backoff_factor=2,
    )
    s.mount(proto + "://", HTTPAdapter(max_retries=retries))

    return s


def get_retriable_https_session(*, tls_verify: bool) -> requests.Session:
    return get_retriable_session("https", tls_verify)


def https_endpoint_is_reachable(url: str, auth: Tuple[str], *, tls_verify: bool) -> bool:
    """
    Checks that `url` is reachable, using `auth` basic credentials.
    """
    return get_retriable_https_session(tls_verify=tls_verify).get(url, auth=auth).status_code == 200

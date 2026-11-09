from typing import Collection, Optional, Tuple

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry


def get_retriable_session(
    proto: str,
    tls_verify: bool,
    status_forcelist: Optional[Collection[int]] = None,
    allowed_methods: Optional[Collection[str]] = Retry.DEFAULT_ALLOWED_METHODS,
) -> requests.Session:
    """
    Returns a request Session object with a retry mechanism.

    This is required to overcome a DNS resolution problem that we have
    experienced in the Evergreen hosts. This can also probably alleviate
    problems arising from request throttling.

    status_forcelist also retries those statuses. allowed_methods=None retries every method, POST included.
    """

    s = requests.Session()

    s.verify = tls_verify
    retries = Retry(
        total=5,
        backoff_factor=2,
        status_forcelist=status_forcelist,
        allowed_methods=allowed_methods,
        raise_on_status=False,
    )
    s.mount(proto + "://", HTTPAdapter(max_retries=retries))

    return s


def get_retriable_https_session(
    *,
    tls_verify: bool,
    status_forcelist: Optional[Collection[int]] = None,
    allowed_methods: Optional[Collection[str]] = Retry.DEFAULT_ALLOWED_METHODS,
) -> requests.Session:
    return get_retriable_session("https", tls_verify, status_forcelist, allowed_methods)


def https_endpoint_is_reachable(url: str, auth: Tuple[str, str], *, tls_verify: bool) -> bool:
    """
    Checks that `url` is reachable, using `auth` basic credentials.
    """
    return get_retriable_https_session(tls_verify=tls_verify).get(url, auth=auth).status_code == 200

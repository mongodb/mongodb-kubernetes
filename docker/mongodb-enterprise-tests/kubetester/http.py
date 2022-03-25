import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry


def get_retriable_session(proto: str = "http") -> requests.Session:
    """
    Returns a request Session object with a retry mechanism.

    This is required to overcome a DNS resolution problem that we have
    experienced in the Evergreen hosts. This can also probably alleviate
    problems arising from request throttling.
    """

    s = requests.Session()

    # do not verify certs at this point.
    s.verify = False
    retries = Retry(
        total=5,
        backoff_factor=2,
    )
    s.mount(proto + "://", HTTPAdapter(max_retries=retries))

    return s


def get_retriable_https_session() -> requests.Session:
    return get_retriable_session("https")

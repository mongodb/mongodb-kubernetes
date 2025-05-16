import random
from string import ascii_lowercase, digits

from .customobject import CustomObject  # noqa: F401
from .kubeobject import KubeObject, create_custom_object  # noqa: F401


def generate_random_name(prefix="", suffix="", size=63) -> str:
    """Generates a random and valid Kubernetes name."""
    max_len = 63
    min_len = 0

    if size > max_len:
        size = max_len

    random_len = size - len(prefix) - len(suffix)
    if random_len < min_len:
        random_len = min_len

    body = []
    if random_len > 0:
        body = [random.choice(ascii_lowercase + digits) for _ in range(random_len - 1)]
        body = [random.choice(ascii_lowercase)] + body

    return prefix + "".join(body) + suffix

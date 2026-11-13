from kubetester.kube_proxy import cluster_proxy_url

CONTEXTS = {"current-context": "a", "contexts": [{"name": "a", "context": {"cluster": "c"}}]}


def test_proxy_url_of_the_cluster_of_a_context():
    config = {**CONTEXTS, "clusters": [{"name": "c", "cluster": {"proxy-url": "http://p:1"}}]}
    assert cluster_proxy_url(config, "a") == "http://p:1"
    assert cluster_proxy_url(config) == "http://p:1"


def test_cluster_without_proxy_url_gives_none():
    config = {**CONTEXTS, "clusters": [{"name": "c", "cluster": {"server": "https://s"}}]}
    assert cluster_proxy_url(config) is None

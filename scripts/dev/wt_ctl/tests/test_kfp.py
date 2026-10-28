from wt_ctl.domains.kfp import suffix_names


def kubeconfig():
    return {
        "clusters": [{"name": "kind-kind", "cluster": {"server": "https://127.0.0.1:41234"}}],
        "users": [{"name": "kind-kind", "user": {}}],
        "contexts": [{"name": "kind-kind", "context": {"cluster": "kind-kind", "user": "kind-kind"}}],
        "current-context": "kind-kind",
    }


def test_every_name_gets_the_proxy_port_and_the_references_still_match():
    result = suffix_names(kubeconfig(), 8005)

    context = result["contexts"][0]
    assert result["clusters"][0]["name"] == context["context"]["cluster"] == "kind-kind-8005"
    assert result["users"][0]["name"] == context["context"]["user"] == "kind-kind-8005"
    assert context["name"] == result["current-context"] == "kind-kind-8005"


def test_the_input_kubeconfig_is_left_as_it_is():
    original = kubeconfig()

    suffix_names(original, 8005)

    assert original == kubeconfig()

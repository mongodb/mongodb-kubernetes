"""VoyageRouter backend membership and hot reload.

The Router selects VoyageAI backends by label and routes only to Running ones. Backend changes reach Envoy
through CDS/LDS file updates, so the Router must follow them without restarting or dropping traffic.
"""

from __future__ import annotations

import pytest
from kubernetes import client
from kubetester import try_load
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.voyageai import VoyageAI, VoyageRouter
from pytest import fixture
from tests.common.voyageai.echo_stub import set_echo_stub, voyage_ai
from tests.common.voyageai.router_client import (
    RERANK,
    TrafficProbe,
    assert_router_error,
    is_echo_from,
    is_router_error,
    model_request,
    ready_router_pods,
    wait_for_response,
    xds_update_counts,
)

ROUTER_NAME = "membership"
SELECTOR_KEY = "voyage-router"
SELECTOR_LABELS = {SELECTOR_KEY: ROUTER_NAME}

EMBED_BACKEND = "embed"
RERANK_BACKEND = "rerank"
EXTRA_BACKEND = "extra"
EMBED_MODEL = "voyage-4"
RERANK_MODEL = "rerank-2.5"
EXTRA_MODEL = "voyage-4-large"

NOT_SELECTED = "model is not selected"


@fixture(scope="module")
def baseline() -> dict[str, dict]:
    return {}


@fixture(scope="module")
def router(namespace: str) -> VoyageRouter:
    resource = VoyageRouter(ROUTER_NAME, namespace)
    try_load(resource)
    return resource


@fixture(scope="module")
def embed(namespace: str, stub_image: str) -> VoyageAI:
    return voyage_ai(namespace, EMBED_BACKEND, EMBED_MODEL, stub_image=stub_image, labels=SELECTOR_LABELS)


@fixture(scope="module")
def rerank(namespace: str, stub_image: str) -> VoyageAI:
    return voyage_ai(namespace, RERANK_BACKEND, RERANK_MODEL, stub_image=stub_image)


@fixture(scope="module")
def extra(namespace: str, stub_image: str) -> VoyageAI:
    return voyage_ai(namespace, EXTRA_BACKEND, EXTRA_MODEL, stub_image=stub_image, labels=SELECTOR_LABELS)


def restart_count(pod: client.V1Pod) -> int:
    return sum(status.restart_count for status in pod.status.container_statuses or [])


@pytest.mark.e2e_voyage_router_membership
def test_install_operator(default_operator: Operator):
    default_operator.wait_for_operator_ready()


@pytest.mark.e2e_voyage_router_membership
def test_create_router(router: VoyageRouter):
    router["spec"] = {"selector": {"matchLabels": SELECTOR_LABELS}}
    router.update()
    router.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_voyage_router_membership
def test_record_router_pods(router: VoyageRouter, baseline: dict[str, dict]):
    pods = ready_router_pods(router)
    assert len(pods) == 1
    baseline["restarts"] = {pod.metadata.name: restart_count(pod) for pod in pods}
    baseline["xds"] = {pod.metadata.name: xds_update_counts(pod) for pod in pods}


@pytest.mark.e2e_voyage_router_membership
def test_model_without_backends_is_not_selected(router: VoyageRouter):
    assert_router_error(model_request(router, EMBED_MODEL)(), 404, NOT_SELECTED)


@pytest.mark.e2e_voyage_router_membership
def test_create_unlabeled_backend(rerank: VoyageAI):
    rerank.update()
    rerank.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_voyage_router_membership
def test_labeled_backend_is_routed(router: VoyageRouter, embed: VoyageAI):
    embed.update()
    embed.assert_reaches_phase(Phase.Running, timeout=600)
    wait_for_response(
        model_request(router, EMBED_MODEL),
        lambda response: is_echo_from(response, EMBED_BACKEND),
        msg="Router to route voyage-4 to the labeled VoyageAI",
    )
    assert_router_error(model_request(router, RERANK_MODEL, RERANK)(), 404, NOT_SELECTED)


@pytest.mark.e2e_voyage_router_membership
def test_labeling_backend_selects_it(router: VoyageRouter, rerank: VoyageAI):
    rerank.load()
    rerank["metadata"].setdefault("labels", {}).update(SELECTOR_LABELS)
    rerank.update()
    wait_for_response(
        model_request(router, RERANK_MODEL, RERANK),
        lambda response: is_echo_from(response, RERANK_BACKEND),
        msg="Router to route rerank-2.5 after its VoyageAI is labeled",
    )


@pytest.mark.e2e_voyage_router_membership
def test_unready_backend_makes_model_unavailable(router: VoyageRouter, embed: VoyageAI, stub_image: str):
    embed.load()
    set_echo_stub(embed, stub_image, ready=False)
    embed.update()
    embed.assert_reaches_phase(Phase.Pending, timeout=300)
    wait_for_response(
        model_request(router, EMBED_MODEL),
        lambda response: is_router_error(response, 503, "model is unavailable"),
        msg="Router to report voyage-4 unavailable while its VoyageAI is not Running",
    )


@pytest.mark.e2e_voyage_router_membership
def test_ready_backend_is_routed_again(router: VoyageRouter, embed: VoyageAI, stub_image: str):
    embed.load()
    set_echo_stub(embed, stub_image, ready=True)
    embed.update()
    embed.assert_reaches_phase(Phase.Running, timeout=600)
    wait_for_response(
        model_request(router, EMBED_MODEL),
        lambda response: is_echo_from(response, EMBED_BACKEND),
        msg="Router to route voyage-4 again once its VoyageAI is Running",
    )


@pytest.mark.e2e_voyage_router_membership
def test_match_expressions_update_backend_selection(router: VoyageRouter, embed: VoyageAI):
    embed.load()
    embed["metadata"].setdefault("labels", {})["e2e-excluded"] = "true"
    embed.update()
    embed.assert_reaches_phase(Phase.Running, timeout=600)

    router.load()
    router["spec"]["selector"] = {"matchExpressions": [{"key": "e2e-excluded", "operator": "DoesNotExist"}]}
    router.update()
    wait_for_response(
        model_request(router, EMBED_MODEL),
        lambda response: is_router_error(response, 404, NOT_SELECTED),
        msg="Router to exclude a VoyageAI that does not match selector expressions",
    )

    embed.load()
    embed["metadata"]["labels"]["e2e-excluded"] = None
    embed.update()
    embed.assert_reaches_phase(Phase.Running, timeout=600)
    wait_for_response(
        model_request(router, EMBED_MODEL),
        lambda response: is_echo_from(response, EMBED_BACKEND),
        msg="Router to select a VoyageAI matching selector expressions",
    )


@pytest.mark.e2e_voyage_router_membership
def test_backend_changes_do_not_disrupt_traffic(router: VoyageRouter, extra: VoyageAI):
    with TrafficProbe(model_request(router, EMBED_MODEL, retry_connect=False)) as probe:
        extra.update()
        extra.assert_reaches_phase(Phase.Running, timeout=600)
        wait_for_response(
            model_request(router, EXTRA_MODEL),
            lambda response: is_echo_from(response, EXTRA_BACKEND),
            msg="Router to route voyage-4-large after its VoyageAI is added",
        )

        extra.delete()
        wait_for_response(
            model_request(router, EXTRA_MODEL),
            lambda response: is_router_error(response, 404, NOT_SELECTED),
            msg="Router to deselect voyage-4-large after its VoyageAI is deleted",
        )

    probe.assert_no_failures()


@pytest.mark.e2e_voyage_router_membership
def test_unlabeling_backend_deselects_it(router: VoyageRouter, rerank: VoyageAI):
    rerank.load()
    # Merge patches only remove keys that are explicitly null.
    rerank["metadata"]["labels"][SELECTOR_KEY] = None
    rerank.update()
    wait_for_response(
        model_request(router, RERANK_MODEL, RERANK),
        lambda response: is_router_error(response, 404, NOT_SELECTED),
        msg="Router to deselect rerank-2.5 after its VoyageAI is unlabeled",
    )


@pytest.mark.e2e_voyage_router_membership
def test_deleting_backend_deselects_it(router: VoyageRouter, embed: VoyageAI):
    embed.delete()
    wait_for_response(
        model_request(router, EMBED_MODEL),
        lambda response: is_router_error(response, 404, NOT_SELECTED),
        msg="Router to deselect voyage-4 after its VoyageAI is deleted",
    )


@pytest.mark.e2e_voyage_router_membership
def test_router_hot_reloaded_without_restarts(router: VoyageRouter, baseline: dict[str, dict]):
    router.assert_reaches_phase(Phase.Running, timeout=60)
    pods = ready_router_pods(router)
    assert {pod.metadata.name: restart_count(pod) for pod in pods} == baseline["restarts"]

    for pod in pods:
        cds_updates, lds_updates = xds_update_counts(pod)
        baseline_cds_updates, baseline_lds_updates = baseline["xds"][pod.metadata.name]
        assert cds_updates > baseline_cds_updates
        assert lds_updates > baseline_lds_updates

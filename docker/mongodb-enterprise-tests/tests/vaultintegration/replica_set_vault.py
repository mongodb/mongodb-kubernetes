from pytest import mark
from kubetester import get_statefulset


@mark.e2e_vault_setup
def test_vault_creation(vault: str, vault_name: str, vault_namespace: str):
    vault

    # assert if vault statefulset is ready, this is sort of redundant(we already assert for pod phase)
    # but this is basic assertion at the moment, will remove in followup PR
    sts = get_statefulset(namespace=vault_namespace, name=vault_name)
    assert sts.status.ready_replicas == 1

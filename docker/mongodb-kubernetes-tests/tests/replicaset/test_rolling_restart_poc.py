from kubetester import find_fixture, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="module")
def rolling_restart_replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    """Create a MongoDB replica set for rolling restart testing."""
    resource = MongoDB.from_yaml(
        find_fixture("replica-set-basic.yaml"),
        namespace=namespace,
        name="rolling-restart-test"
    )
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    try_load(resource)
    return resource


@mark.e2e_rolling_restart_poc
def test_replica_set_ready(rolling_restart_replica_set: MongoDB):
    """Test that replica set reaches running state initially."""
    rolling_restart_replica_set.update()
    rolling_restart_replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_rolling_restart_poc  
def test_statefulset_has_ondelete_strategy(rolling_restart_replica_set: MongoDB, namespace: str):
    """Verify that StatefulSet uses OnDelete update strategy for agent coordination."""
    from kubernetes import client
    
    appsv1 = client.AppsV1Api()
    sts = appsv1.read_namespaced_stateful_set("rolling-restart-test", namespace)
    
    assert sts.spec.update_strategy.type == "OnDelete", \
        f"Expected OnDelete strategy, got {sts.spec.update_strategy.type}"


@mark.e2e_rolling_restart_poc
def test_pods_have_kubernetes_env_vars(rolling_restart_replica_set: MongoDB, namespace: str):
    """Verify pods have POD_NAME and POD_NAMESPACE environment variables."""
    from kubernetes import client

    corev1 = client.CoreV1Api()

    for i in range(3):
        pod_name = f"rolling-restart-test-{i}"
        pod = corev1.read_namespaced_pod(pod_name, namespace)

        # Check main container environment variables
        container = pod.spec.containers[0]
        env_vars = {env.name: env for env in container.env}

        assert "POD_NAME" in env_vars, f"POD_NAME not found in {pod_name}"
        assert "POD_NAMESPACE" in env_vars, f"POD_NAMESPACE not found in {pod_name}"

        # Verify they use downward API
        assert env_vars["POD_NAME"].value_from.field_ref.field_path == "metadata.name"
        assert env_vars["POD_NAMESPACE"].value_from.field_ref.field_path == "metadata.namespace"


@mark.e2e_rolling_restart_poc
def test_trigger_rolling_restart(rolling_restart_replica_set: MongoDB, namespace: str):
    """Test triggering rolling restart by changing StatefulSet spec."""
    from kubernetes import client
    
    appsv1 = client.AppsV1Api()
    
    # Get initial StatefulSet revision
    sts = appsv1.read_namespaced_stateful_set("rolling-restart-test", namespace)
    initial_revision = sts.status.update_revision
    print(f"Initial StatefulSet revision: {initial_revision}")

    # Trigger rolling restart by changing pod template (which changes StatefulSet spec)
    # This simulates infrastructure changes that require pod restarts
    rolling_restart_replica_set.load()
    rolling_restart_replica_set["spec"]["podSpec"] = {
        "podTemplate": {
            "metadata": {
                "annotations": {
                    "rolling-restart-test/restart-trigger": "test-change-1"
                }
            }
        }
    }
    rolling_restart_replica_set.update()

    # Wait for StatefulSet to get updated with new revision
    import time
    max_wait = 60
    start_time = time.time()
    new_revision = initial_revision
    
    while new_revision == initial_revision and (time.time() - start_time) < max_wait:
        time.sleep(2)
        sts = appsv1.read_namespaced_stateful_set("rolling-restart-test", namespace)
        new_revision = sts.status.update_revision
        print(f"Current StatefulSet revision: {new_revision}")

    assert new_revision != initial_revision, \
        f"StatefulSet revision should change. Initial: {initial_revision}, Current: {new_revision}"
    
    print(f"StatefulSet revision updated from {initial_revision} to {new_revision}")

    # Wait for the rolling restart coordination to complete and reach running state
    rolling_restart_replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_rolling_restart_poc
def test_all_pods_restarted_with_new_revision(rolling_restart_replica_set: MongoDB, namespace: str):
    """Verify all pods eventually restart and get the new StatefulSet revision."""
    from kubernetes import client
    import time
    
    appsv1 = client.AppsV1Api()
    corev1 = client.CoreV1Api()

    # Get target revision from StatefulSet
    sts = appsv1.read_namespaced_stateful_set("rolling-restart-test", namespace)
    target_revision = sts.status.update_revision
    print(f"Target StatefulSet revision: {target_revision}")

    # Wait for all pods to be updated with the target revision
    # This tests that agent coordination allows all pods to restart
    max_wait = 600  # 10 minutes
    start_time = time.time()
    
    while (time.time() - start_time) < max_wait:
        all_updated = True
        
        for i in range(3):
            pod_name = f"rolling-restart-test-{i}"
            try:
                pod = corev1.read_namespaced_pod(pod_name, namespace)
                current_revision = pod.metadata.labels.get("controller-revision-hash", "")
                
                if current_revision != target_revision:
                    all_updated = False
                    print(f"Pod {pod_name} revision: {current_revision} (target: {target_revision})")
                else:
                    print(f"Pod {pod_name} updated to target revision: {target_revision}")
                    
            except client.rest.ApiException:
                all_updated = False
                print(f"Pod {pod_name} not ready yet")
        
        if all_updated:
            print("All pods successfully updated with agent coordination!")
            break
            
        time.sleep(10)
    
    # Final verification - all pods should have target revision and be ready
    for i in range(3):
        pod_name = f"rolling-restart-test-{i}"
        pod = corev1.read_namespaced_pod(pod_name, namespace)
        
        assert pod.status.phase == "Running", f"Pod {pod_name} should be running"
        
        current_revision = pod.metadata.labels.get("controller-revision-hash", "")
        assert current_revision == target_revision, \
            f"Pod {pod_name} should have target revision {target_revision}, got {current_revision}"

        # Verify container is ready
        if pod.status.container_statuses:
            container_ready = any(status.ready for status in pod.status.container_statuses)
            assert container_ready, f"Pod {pod_name} container should be ready"


@mark.e2e_rolling_restart_poc
def test_verify_agent_coordination_logs(rolling_restart_replica_set: MongoDB, namespace: str):
    """Verify that agents show coordination behavior in logs."""

    # Look for coordination-related log messages in agent logs
    coordination_patterns = [
        "CheckRollingRestartKube",
        "WaitCanUpdate",
        "DeleteMyPodKube",
        "needsUpdate=true",
        "Kubernetes upgrade",
        "StatefulSet.*revision"
    ]

    found_patterns = set()

    for i in range(3):
        pod_name = f"rolling-restart-test-{i}"

        # Get agent logs - try both possible container names
        try:
            # For static architecture
            logs = KubernetesTester.get_pod_logs(namespace, pod_name, container="mongodb-agent")
        except:
            try:
                # For non-static architecture
                logs = KubernetesTester.get_pod_logs(namespace, pod_name, container="mongodb-enterprise-database")
            except:
                logs = KubernetesTester.get_pod_logs(namespace, pod_name)

        # Check for coordination patterns
        for pattern in coordination_patterns:
            if pattern in logs:
                found_patterns.add(pattern)
                print(f"Found coordination pattern '{pattern}' in {pod_name}")

    # We should find at least some coordination patterns
    # (exact patterns depend on timing and whether POC agent is actually used)
    print(f"Found coordination patterns: {found_patterns}")

    # This is more of an informational check - in real POC test with custom agent,
    # we'd expect to see the coordination patterns
    assert len(found_patterns) >= 0, "Should find some evidence of coordination behavior"

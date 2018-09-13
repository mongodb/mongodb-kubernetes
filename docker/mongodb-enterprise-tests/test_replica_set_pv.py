import pytest
from kubetester import KubernetesTester
from kubernetes import client


@pytest.mark.replica_set
@pytest.mark.create
class TestReplicaSetCreation(KubernetesTester):
    '''
    name: Replica Set Creation with PersistentVolumes
    description: |
      Creates a Replica Set and allocates a PersistentVolume to it.
    create:
      file: fixtures/replica-set-pv.yaml
      wait_for: 120
    '''

    def test_replica_set_sts_exists(self):
        sts = self.appsv1.read_namespaced_stateful_set('rs001-pv', self.namespace)
        assert sts

    def test_sts_creation(self):
        sts = self.appsv1.read_namespaced_stateful_set('rs001-pv', self.namespace)

        assert sts.api_version == 'apps/v1'
        assert sts.kind == 'StatefulSet'
        assert sts.status.current_replicas == 3
        assert sts.status.ready_replicas == 3

    def test_sts_metadata(self):
        sts = self.appsv1.read_namespaced_stateful_set('rs001-pv', self.namespace)

        assert sts.metadata.name == 'rs001-pv'
        assert sts.metadata.labels['app'] == 'rs001-pv-svc'
        assert sts.metadata.namespace == self.namespace
        owner_ref0 = sts.metadata.owner_references[0]
        assert owner_ref0.api_version == 'mongodb.com/v1'
        assert owner_ref0.kind == 'MongoDbReplicaSet'
        assert owner_ref0.name == 'rs001-pv'

    def test_sts_replicas(self):
        sts = self.appsv1.read_namespaced_stateful_set('rs001-pv', self.namespace)
        assert sts.spec.replicas == 3

    def test_sts_template(self):
        sts = self.appsv1.read_namespaced_stateful_set('rs001-pv', self.namespace)

        tmpl = sts.spec.template
        assert tmpl.metadata.labels['app'] == 'rs001-pv-svc'
        assert tmpl.metadata.labels['controller'] == 'mongodb-enterprise-operator'
        assert tmpl.spec.affinity.node_affinity is None
        assert tmpl.spec.affinity.pod_affinity is None
        assert tmpl.spec.affinity.pod_anti_affinity is not None

    def _get_pods(self, podname, qty=3):
        return [podname.format(i) for i in range(qty)]

    def test_replica_set_pods_exists(self):
        for podname in self._get_pods('rs001-pv-{}', 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            assert pod.metadata.name == podname

    def test_pods_are_running(self):
        for podname in self._get_pods('rs001-pv-{}', 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            assert pod.status.phase == 'Running'

    def test_pods_containers(self):
        for podname in self._get_pods('rs001-pv-{}', 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            assert c0.name == 'mongodb-enterprise-database'

    def test_pods_containers_ports(self):
        for podname in self._get_pods('rs001-pv-{}', 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            c0.ports[0].container_port == 27017
            c0.ports[0].host_ip is None
            c0.ports[0].host_port is None
            c0.ports[0].protocol == 'TCP'

    def test_pods_container_envvars(self):
        for podname in self._get_pods('rs001-pv-{}', 3):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            for envvar in c0.env:
                assert envvar.name in ['BASE_URL', 'GROUP_ID', 'USER_LOGIN', 'AGENT_API_KEY']
                assert envvar.value is not None

    def test_service_is_created(self):
        svc = self.corev1.read_namespaced_service('rs001-pv-svc', self.namespace)
        assert svc

    def test_om_processes_are_created(self):
        config = self.get_automation_config()
        assert len(config['processes']) == 3

    def test_om_replica_set_is_created(self):
        config = self.get_automation_config()
        assert len(config['replicaSets']) == 1

    def test_om_processes(self):
        config = self.get_automation_config()
        processes = config['processes']
        p0 = processes[0]
        p1 = processes[1]
        p2 = processes[2]

        # First Process
        assert p0['name'] == 'rs001-pv-0'
        assert p0['processType'] == 'mongod'
        assert p0['version'] == '4.0.1'
        assert p0['authSchemaVersion'] == 5
        assert p0['featureCompatibilityVersion'] == '4.0'
        assert p0['hostname'] == 'rs001-pv-0.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert p0['args2_6']['net']['port'] == 27017
        assert p0['args2_6']['replication']['replSetName'] == 'rs001-pv'
        assert p0['args2_6']['storage']['dbPath'] == '/data'
        assert p0['args2_6']['systemLog']['destination'] == 'file'
        assert p0['args2_6']['systemLog']['path'] == '/data/mongodb.log'
        assert p0['logRotate']['sizeThresholdMB'] == 1000
        assert p0['logRotate']['timeThresholdHrs'] == 24

        # Second Process
        assert p1['name'] == 'rs001-pv-1'
        assert p1['processType'] == 'mongod'
        assert p1['version'] == '4.0.1'
        assert p1['authSchemaVersion'] == 5
        assert p1['featureCompatibilityVersion'] == '4.0'
        assert p1['hostname'] == 'rs001-pv-1.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert p1['args2_6']['net']['port'] == 27017
        assert p1['args2_6']['replication']['replSetName'] == 'rs001-pv'
        assert p1['args2_6']['storage']['dbPath'] == '/data'
        assert p1['args2_6']['systemLog']['destination'] == 'file'
        assert p1['args2_6']['systemLog']['path'] == '/data/mongodb.log'
        assert p1['logRotate']['sizeThresholdMB'] == 1000
        assert p1['logRotate']['timeThresholdHrs'] == 24

        # Third Process
        assert p2['name'] == 'rs001-pv-2'
        assert p2['processType'] == 'mongod'
        assert p2['version'] == '4.0.1'
        assert p2['authSchemaVersion'] == 5
        assert p2['featureCompatibilityVersion'] == '4.0'
        assert p2['hostname'] == 'rs001-pv-2.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert p2['args2_6']['net']['port'] == 27017
        assert p2['args2_6']['replication']['replSetName'] == 'rs001-pv'
        assert p2['args2_6']['storage']['dbPath'] == '/data'
        assert p2['args2_6']['systemLog']['destination'] == 'file'
        assert p2['args2_6']['systemLog']['path'] == '/data/mongodb.log'
        assert p2['logRotate']['sizeThresholdMB'] == 1000
        assert p2['logRotate']['timeThresholdHrs'] == 24

    def test_om_replica_set(self):
        config = self.get_automation_config()
        rs = config['replicaSets']
        assert rs[0]['_id'] == 'rs001-pv'
        m0 = rs[0]['members'][0]
        m1 = rs[0]['members'][1]
        m2 = rs[0]['members'][2]

        # First Member
        assert m0['_id'] == 0
        assert m0['arbiterOnly'] is False
        assert m0['hidden'] is False
        assert m0['priority'] == 1
        assert m0['slaveDelay'] == 0
        assert m0['votes'] == 1
        assert m0['buildIndexes'] is True
        assert m0['host'] == 'rs001-pv-0'

        # Second Member
        assert m1['_id'] == 1
        assert m1['arbiterOnly'] is False
        assert m1['hidden'] is False
        assert m1['priority'] == 1
        assert m1['slaveDelay'] == 0
        assert m1['votes'] == 1
        assert m1['buildIndexes'] is True
        assert m1['host'] == 'rs001-pv-1'

        # Third Member
        assert m2['_id'] == 2
        assert m2['arbiterOnly'] is False
        assert m2['hidden'] is False
        assert m2['priority'] == 1
        assert m2['slaveDelay'] == 0
        assert m2['votes'] == 1
        assert m2['buildIndexes'] is True
        assert m2['host'] == 'rs001-pv-2'

    def test_monitoring_versions(self):
        config = self.get_automation_config()
        mv = config['monitoringVersions']
        assert mv[0]['baseUrl'] is None
        # Monitoring agent is installed in first host
        hostname = 'rs001-pv-0.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert mv[0]['hostname'] == hostname

        # TODO: from where should we get this version?
        assert mv[0]['name'] == '6.4.0.433-1'

    def test_backup(self):
        config = self.get_automation_config()
        # 1 backup agent per host
        assert len(config['backupVersions']) == 3
        bkp = config['backupVersions']

        # TODO: from where should we get this version?
        assert bkp[0]['name'] == '6.6.0.959-1'
        hostname = 'rs001-pv-0.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert bkp[0]['hostname'] == hostname

        assert bkp[1]['name'] == '6.6.0.959-1'
        hostname = 'rs001-pv-1.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert bkp[1]['hostname'] == hostname

        assert bkp[2]['name'] == '6.6.0.959-1'
        hostname = 'rs001-pv-2.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert bkp[2]['hostname'] == hostname


@pytest.mark.replica_set
@pytest.mark.update
class TestReplicaSetUpdate(KubernetesTester):
    '''
    name: Replica Set Updates
    description: |
      Updates a Replica Set to 5 members.
    update:
      file: fixtures/replica-set.yaml
      patch: '[{"op":"replace","path":"/spec/members","value":5}]'
      wait_for: 60
    '''
    def test_replica_set_sts_should_exist(self):
        sts = self.appsv1.read_namespaced_stateful_set('rs001-pv', self.namespace)
        assert sts

    def test_sts_update(self):
        sts = self.appsv1.read_namespaced_stateful_set('rs001-pv', self.namespace)

        assert sts.api_version == 'apps/v1'
        assert sts.kind == 'StatefulSet'
        assert sts.status.current_replicas == 5
        assert sts.status.ready_replicas == 5

    def test_sts_metadata(self):
        sts = self.appsv1.read_namespaced_stateful_set('rs001-pv', self.namespace)

        assert sts.metadata.name == 'rs001-pv'
        assert sts.metadata.labels['app'] == 'rs001-pv-svc'
        assert sts.metadata.namespace == self.namespace
        owner_ref0 = sts.metadata.owner_references[0]
        assert owner_ref0.api_version == 'mongodb.com/v1'
        assert owner_ref0.kind == 'MongoDbReplicaSet'
        assert owner_ref0.name == 'rs001-pv'

    def test_sts_replicas(self):
        sts = self.appsv1.read_namespaced_stateful_set('rs001-pv', self.namespace)
        assert sts.spec.replicas == 5

    def _get_pods(self, podname, qty):
        return [podname.format(i) for i in range(qty)]

    def test_replica_set_pods_exists(self):
        for podname in self._get_pods('rs001-pv-{}', 5):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            assert pod.metadata.name == podname

    def test_pods_are_running(self):
        for podname in self._get_pods('rs001-pv-{}', 5):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            assert pod.status.phase == 'Running'

    def test_pods_containers(self):
        for podname in self._get_pods('rs001-pv-{}', 5):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            assert c0.name == 'mongodb-enterprise-database'

    def test_pods_containers_ports(self):
        for podname in self._get_pods('rs001-pv-{}', 5):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            c0.ports[0].container_port == 27017
            c0.ports[0].host_ip is None
            c0.ports[0].host_port is None
            c0.ports[0].protocol == 'TCP'

    def test_pods_container_envvars(self):
        for podname in self._get_pods('rs001-pv-{}', 5):
            pod = self.corev1.read_namespaced_pod(podname, self.namespace)
            c0 = pod.spec.containers[0]
            for envvar in c0.env:
                assert envvar.name in ['BASE_URL', 'GROUP_ID', 'USER_LOGIN', 'AGENT_API_KEY']
                assert envvar.value is not None

    def test_service_is_created(self):
        svc = self.corev1.read_namespaced_service('rs001-pv-svc', self.namespace)
        assert svc

    def test_om_processes_are_created(self):
        config = self.get_automation_config()
        assert len(config['processes']) == 5

    def test_om_replica_set_is_created(self):
        config = self.get_automation_config()
        assert len(config['replicaSets']) == 1

    def test_om_processes(self):
        config = self.get_automation_config()
        processes = config['processes']
        p0 = processes[0]
        p1 = processes[1]
        p2 = processes[2]
        p3 = processes[3]
        p4 = processes[4]

        # First Process
        assert p0['name'] == 'rs001-pv-0'
        assert p0['processType'] == 'mongod'
        assert p0['version'] == '4.0.1'
        assert p0['authSchemaVersion'] == 5
        assert p0['featureCompatibilityVersion'] == '4.0'
        assert p0['hostname'] == 'rs001-pv-0.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert p0['args2_6']['net']['port'] == 27017
        assert p0['args2_6']['replication']['replSetName'] == 'rs001-pv'
        assert p0['args2_6']['storage']['dbPath'] == '/data'
        assert p0['args2_6']['systemLog']['destination'] == 'file'
        assert p0['args2_6']['systemLog']['path'] == '/data/mongodb.log'
        assert p0['logRotate']['sizeThresholdMB'] == 1000
        assert p0['logRotate']['timeThresholdHrs'] == 24

        # Second Process
        assert p1['name'] == 'rs001-pv-1'
        assert p1['processType'] == 'mongod'
        assert p1['version'] == '4.0.1'
        assert p1['authSchemaVersion'] == 5
        assert p1['featureCompatibilityVersion'] == '4.0'
        assert p1['hostname'] == 'rs001-pv-1.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert p1['args2_6']['net']['port'] == 27017
        assert p1['args2_6']['replication']['replSetName'] == 'rs001-pv'
        assert p1['args2_6']['storage']['dbPath'] == '/data'
        assert p1['args2_6']['systemLog']['destination'] == 'file'
        assert p1['args2_6']['systemLog']['path'] == '/data/mongodb.log'
        assert p1['logRotate']['sizeThresholdMB'] == 1000
        assert p1['logRotate']['timeThresholdHrs'] == 24

        # Third Process
        assert p2['name'] == 'rs001-pv-2'
        assert p2['processType'] == 'mongod'
        assert p2['version'] == '4.0.1'
        assert p2['authSchemaVersion'] == 5
        assert p2['featureCompatibilityVersion'] == '4.0'
        assert p2['hostname'] == 'rs001-pv-2.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert p2['args2_6']['net']['port'] == 27017
        assert p2['args2_6']['replication']['replSetName'] == 'rs001-pv'
        assert p2['args2_6']['storage']['dbPath'] == '/data'
        assert p2['args2_6']['systemLog']['destination'] == 'file'
        assert p2['args2_6']['systemLog']['path'] == '/data/mongodb.log'
        assert p2['logRotate']['sizeThresholdMB'] == 1000
        assert p2['logRotate']['timeThresholdHrs'] == 24

        # Fourth Process
        assert p3['name'] == 'rs001-pv-3'
        assert p3['processType'] == 'mongod'
        assert p3['version'] == '4.0.1'
        assert p3['authSchemaVersion'] == 5
        assert p3['featureCompatibilityVersion'] == '4.0'
        assert p3['hostname'] == 'rs001-pv-3.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert p3['args2_6']['net']['port'] == 27017
        assert p3['args2_6']['replication']['replSetName'] == 'rs001-pv'
        assert p3['args2_6']['storage']['dbPath'] == '/data'
        assert p3['args2_6']['systemLog']['destination'] == 'file'
        assert p3['args2_6']['systemLog']['path'] == '/data/mongodb.log'
        assert p3['logRotate']['sizeThresholdMB'] == 1000
        assert p3['logRotate']['timeThresholdHrs'] == 24

        # Fifth Process
        assert p4['name'] == 'rs001-pv-4'
        assert p4['processType'] == 'mongod'
        assert p4['version'] == '4.0.1'
        assert p4['authSchemaVersion'] == 5
        assert p4['featureCompatibilityVersion'] == '4.0'
        assert p4['hostname'] == 'rs001-pv-4.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)
        assert p4['args2_6']['net']['port'] == 27017
        assert p4['args2_6']['replication']['replSetName'] == 'rs001-pv'
        assert p4['args2_6']['storage']['dbPath'] == '/data'
        assert p4['args2_6']['systemLog']['destination'] == 'file'
        assert p4['args2_6']['systemLog']['path'] == '/data/mongodb.log'
        assert p4['logRotate']['sizeThresholdMB'] == 1000
        assert p4['logRotate']['timeThresholdHrs'] == 24

    def test_om_replica_set(self):
        config = self.get_automation_config()
        rs = config['replicaSets']
        assert rs[0]['_id'] == 'rs001-pv'
        m0 = rs[0]['members'][0]
        m1 = rs[0]['members'][1]
        m2 = rs[0]['members'][2]
        m3 = rs[0]['members'][3]
        m4 = rs[0]['members'][4]

        # First Member
        assert m0['_id'] == 0
        assert m0['arbiterOnly'] is False
        assert m0['hidden'] is False
        assert m0['priority'] == 1
        assert m0['slaveDelay'] == 0
        assert m0['votes'] == 1
        assert m0['buildIndexes'] is True
        assert m0['host'] == 'rs001-pv-0'

        # Second Member
        assert m1['_id'] == 1
        assert m1['arbiterOnly'] is False
        assert m1['hidden'] is False
        assert m1['priority'] == 1
        assert m1['slaveDelay'] == 0
        assert m1['votes'] == 1
        assert m1['buildIndexes'] is True
        assert m1['host'] == 'rs001-pv-1'

        # Third Member
        assert m2['_id'] == 2
        assert m2['arbiterOnly'] is False
        assert m2['hidden'] is False
        assert m2['priority'] == 1
        assert m2['slaveDelay'] == 0
        assert m2['votes'] == 1
        assert m2['buildIndexes'] is True
        assert m2['host'] == 'rs001-pv-2'

        # Fourth Member
        assert m3['_id'] == 3
        assert m3['arbiterOnly'] is False
        assert m3['hidden'] is False
        assert m3['priority'] == 1
        assert m3['slaveDelay'] == 0
        assert m3['votes'] == 1
        assert m3['buildIndexes'] is True
        assert m3['host'] == 'rs001-pv-3'

        # Fifth Member
        assert m4['_id'] == 4
        assert m4['arbiterOnly'] is False
        assert m4['hidden'] is False
        assert m4['priority'] == 1
        assert m4['slaveDelay'] == 0
        assert m4['votes'] == 1
        assert m4['buildIndexes'] is True
        assert m4['host'] == 'rs001-pv-4'

    def test_monitoring_versions(self):
        config = self.get_automation_config()
        mv = config['monitoringVersions']
        assert mv[0]['baseUrl'] is None
        # Monitoring agent is installed in first host
        assert mv[0]['hostname'] == 'rs001-pv-0.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)

        # TODO: from where should we get this version?
        assert mv[0]['name'] == '6.4.0.433-1'

    def test_backup(self):
        config = self.get_automation_config()
        # 1 backup agent per host
        assert len(config['backupVersions']) == 5
        bkp = config['backupVersions']

        # TODO: from where should we get this version?
        assert bkp[0]['name'] == '6.6.0.959-1'
        assert bkp[0]['hostname'] == 'rs001-pv-0.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)

        assert bkp[1]['name'] == '6.6.0.959-1'
        assert bkp[1]['hostname'] == 'rs001-pv-1.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)

        assert bkp[2]['name'] == '6.6.0.959-1'
        assert bkp[2]['hostname'] == 'rs001-pv-2.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)

        assert bkp[3]['name'] == '6.6.0.959-1'
        assert bkp[3]['hostname'] == 'rs001-pv-3.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)

        assert bkp[4]['name'] == '6.6.0.959-1'
        assert bkp[4]['hostname'] == 'rs001-pv-4.rs001-pv-svc.{}.svc.cluster.local'.format(self.namespace)


@pytest.mark.replica_set
@pytest.mark.delete
class TestReplicaSetDelete(KubernetesTester):
    '''
    name: Replica Set Deletion
    description: |
      Deletes a Replica Set.
    delete:
      file: fixtures/replica-set-pv.yaml
      wait_for: 120
    '''
    def test_replica_set_sts_doesnt_exist(self):
        'StatefulSet should not exist'
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set('rs001-pv', self.namespace)

    def test_service_does_not_exist(self):
        'Services should not exist'
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service('rs001-pv-svc', self.namespace)

    def test_om_replica_set_is_deleted(self):
        config = self.get_automation_config()
        assert len(config['replicaSets']) == 0

    def test_om_processes_are_deleted(self):
        config = self.get_automation_config()
        assert len(config['processes']) == 0

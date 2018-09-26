package operator

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
)

func (c *MongoDbController) onAddStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone)

	log := zap.S().With("standalone", s.Name)

	defer exceptionHandling("Failed to create Mongodb Standalone", log)

	log.Infow(">> Creating Mongodb Standalone", "config", s.Spec)

	conn, err := c.doStandaloneProcessing(nil, s, log)
	if err != nil {
		log.Errorf("Failed to create Mongodb Standalone: %s", err)
		return
	}

	log.Infof("Created MongoDb Standalone! %s", completionMessage(conn.BaseUrl(), conn.GroupId()))
}

func (c *MongoDbController) onUpdateStandalone(oldObj, newObj interface{}) {
	o := oldObj.(*mongodb.MongoDbStandalone)
	n := newObj.(*mongodb.MongoDbStandalone)
	log := zap.S().With("standalone", n.Name)

	defer exceptionHandling("Failed to update Mongodb Standalone", log)

	log.Infow(">> Updating MongoDbStandalone", "oldConfig", o.Spec, "newConfig", n.Spec)

	if err := validateUpdateStandalone(o, n); err != nil {
		log.Error(err)
		return
	}

	conn, err := c.doStandaloneProcessing(nil, n, log)
	if err != nil {
		log.Errorf("Failed to update Mongodb Standalone: %s", err)
		return
	}

	log.Infof("Updated MongoDbStandalone! %s", completionMessage(conn.BaseUrl(), conn.GroupId()))
}

func (c *MongoDbController) onDeleteStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone)
	log := zap.S().With("Standalone", s.Name)

	defer exceptionHandling("Failed to delete Mongodb Standalone", log)

	log.Infow(">> Deleting MongoDbStandalone", "config", s.Spec)

	conn, _, err := c.prepareOmConnection(s.Namespace, s.Spec.Project, s.Spec.Credentials, log)
	if err != nil {
		log.Error(err)
		return
	}

	err = conn.ReadUpdateDeployment(false,
		func(d om.Deployment) error {
			if err = d.RemoveProcessByName(s.Name); err != nil {
				return err
			}

			return nil
		},
	)
	if err != nil {
		log.Errorf("Failed to update Ops Manager automation config: %s", err)
	}

	hostsToRemove, _ := GetDnsNames(s.Name, s.ServiceName(), s.Namespace, s.Spec.ClusterName, 1)
	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err := om.StopMonitoring(conn, hostsToRemove, log); err != nil {
		log.Errorf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err)
		return
	}

	log.Info("Removed!")
}

func (c *MongoDbController) doStandaloneProcessing(o, n *mongodb.MongoDbStandalone, log *zap.SugaredLogger) (om.OmConnection, error) {
	spec := n.Spec
	conn, podVars, err := c.prepareOmConnection(n.Namespace, spec.Project, spec.Credentials, log)
	if err != nil {
		return nil, err
	}

	standaloneBuilder := c.kubeHelper.NewStatefulSetHelper(n).
		SetService(n.ServiceName()).
		SetPersistence(n.Spec.Persistent).
		SetPodSpec(NewDefaultStandalonePodSpecWrapper(n.Spec.PodSpec)).
		SetPodVars(podVars).
		SetExposedExternally(true).
		SetLogger(log)

	err = standaloneBuilder.CreateOrUpdateInKubernetes()
	if err != nil {
		return nil, fmt.Errorf("Failed to create statefulset: %s", err)
	}

	if err := updateOmDeployment(conn, n, standaloneBuilder.BuildStatefulSet(), log); err != nil {
		return nil, fmt.Errorf("Failed to create standalone in Ops Manager: %s", err)
	}
	return conn, nil
}

func updateOmDeployment(omConnection om.OmConnection, s *mongodb.MongoDbStandalone,
	set *appsv1.StatefulSet, log *zap.SugaredLogger) error {
	if err := waitForRsAgentsToRegister(set, s.Spec.ClusterName, omConnection, log); err != nil {
		return err
	}

	standaloneOmObject := createProcess(set, s)
	err := omConnection.ReadUpdateDeployment(false,
		func(d om.Deployment) error {
			d.MergeStandalone(standaloneOmObject, nil)
			d.AddMonitoringAndBackup(standaloneOmObject.HostName(), log)

			return nil
		},
	)
	if err != nil {
		return err
	}

	return nil
}

func validateUpdateStandalone(oldSpec, newSpec *mongodb.MongoDbStandalone) error {
	if newSpec.Namespace != oldSpec.Namespace {
		return errors.New("Namespace cannot change for existing object")
	}

	if newSpec.Spec.ClusterName != oldSpec.Spec.ClusterName {
		return errors.New("Cluster Name cannot change for existing object")
	}

	return nil
}

func createProcess(set *appsv1.StatefulSet, s *mongodb.MongoDbStandalone) om.Process {
	hostnames, _ := GetDnsForStatefulSet(set, s.Spec.ClusterName)
	wiredTigerCache := calculateWiredTigerCache(set)

	process := om.NewMongodProcess(s.Name, hostnames[0], s.Spec.Version)
	if wiredTigerCache != nil {
		process.SetWiredTigerCache(*wiredTigerCache)
	}
	return process
}

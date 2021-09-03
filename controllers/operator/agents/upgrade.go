package agents

import (
	"context"
	"fmt"
	"sync"
	"time"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var nextScheduledTime time.Time

const pause = time.Hour * 24

var mux sync.Mutex

func init() {
	ScheduleUpgrade()
}

// UpgradeAllIfNeeded performs the upgrade of agents for all the MongoDB resources registered in the system if necessary
// It's designed to be run "in background" - so must not break any existing reconciliations it's triggered from and
// so doesn't return errors
// Concurrency behavior: the mutex is used to:
// 1. ensure no separate routines invoke the upgrade in parallel
// 2. different reconciliations started in parallel (e.g. Operator has restarted) wait for the upgrade procedure to happen
// for all existing MongoDB resources before proceeding. This could be a critical thing when the major version OM upgrade
// happens and all existing MongoDBs are required to get agents upgraded (otherwise the "You need to upgrade the
// automation agent before publishing other changes" error happens for automation config pushes from the Operator)
func UpgradeAllIfNeeded(client kubernetesClient.Client, omConnectionFactory om.ConnectionFactory, watchNamespace string) {
	mux.Lock()
	defer mux.Unlock()

	if !time.Now().After(nextScheduledTime) {
		return
	}
	log := zap.S()
	log.Info("Performing a regular upgrade of Agents for all the MongoDB resources in the cluster...")
	allMDBs, err := readAllMongoDBs(client, watchNamespace)
	if err != nil {
		log.Errorf("Failed to read MongoDB resources to ensure Agents have the latest version: %s", err)
		return
	}

	err = doUpgrade(client, omConnectionFactory, allMDBs)
	if err != nil {
		log.Errorf("Failed to perform upgrade of Agents: %s", err)
	}
	log.Info("The upgrade of Agents for all the MongoDB resources in the cluster is finished.")

	nextScheduledTime = nextScheduledTime.Add(pause)
}

// ScheduleUpgrade allows to reset the timer to Now() which makes sure the next MongoDB reconciliation will ensure
// all the watched agents are up-to-date.
// This is needed for major/minor OM upgrades as all dependent MongoDBs won't get reconciled with "You need to upgrade the
// automation agent before publishing other changes"
func ScheduleUpgrade() {
	nextScheduledTime = time.Now()
}

// NextScheduledUpgradeTime returns the next scheduled time. Mostly needed for testing.
func NextScheduledUpgradeTime() time.Time {
	return nextScheduledTime
}

func doUpgrade(cl kubernetesClient.Client, factory om.ConnectionFactory, mdbs []mdbv1.MongoDB) error {
	for _, mdb := range mdbs {
		log := zap.S().With(string(mdb.Spec.ResourceType), mdb.ObjectKey())
		conn, err := connectToMongoDB(cl, factory, mdb, log)
		if err != nil {
			log.Warnf("Failed to establish connection to Ops Manager to perform Agent upgrade: %s", err)
			continue
		}

		currentVersion := ""
		if deployment, err := conn.ReadDeployment(); err == nil {
			currentVersion = deployment.GetAgentVersion()
		}
		version, err := conn.UpgradeAgentsToLatest()
		if err != nil {
			log.Warnf("Failed to schedule Agent upgrade: %s, this could be due do ongoing Automation Config publishing in Ops Manager and will get fixed during next trial", err)
			continue
		}
		if currentVersion != version && currentVersion != "" {
			log.Debugf("Submitted the request to Ops Manager to upgrade the agents from %s to the latest version (%s)", currentVersion, version)
		}
	}
	return nil
}

func readAllMongoDBs(cl client.Client, watchNamespace string) ([]mdbv1.MongoDB, error) {
	var namespaces []string

	// 1. Read all namespaces to traverse. This will be a single namespace in case of a namespaced Operator
	if watchNamespace == "*" {
		namespaceList := corev1.NamespaceList{}
		if err := cl.List(context.TODO(), &namespaceList); err != nil {
			return []mdbv1.MongoDB{}, err
		}
		for _, item := range namespaceList.Items {
			namespaces = append(namespaces, item.Name)
		}
	} else {
		namespaces = append(namespaces, watchNamespace)
	}

	mdbs := []mdbv1.MongoDB{}
	// 2. Find all MongoDBs in the namespaces
	for _, ns := range namespaces {
		mongodbList := mdbv1.MongoDBList{}
		if err := cl.List(context.TODO(), &mongodbList, client.InNamespace(ns)); err != nil {
			return []mdbv1.MongoDB{}, err
		}
		mdbs = append(mdbs, mongodbList.Items...)
	}
	return mdbs, nil
}

type secretAndConfigMapGetter interface {
	configmap.Getter
	secret.Getter
}

func connectToMongoDB(getter secretAndConfigMapGetter, factory om.ConnectionFactory, mdb mdbv1.MongoDB, log *zap.SugaredLogger) (om.Connection, error) {
	projectConfig, err := project.ReadProjectConfig(getter, kube.ObjectKey(mdb.Namespace, mdb.Spec.GetProject()), mdb.Name)
	if err != nil {
		return nil, fmt.Errorf("Error reading Project Config: %s", err)
	}
	credsConfig, err := project.ReadCredentials(getter, kube.ObjectKey(mdb.Namespace, mdb.Spec.Credentials), log)
	if err != nil {
		return nil, fmt.Errorf("Error reading Credentials secret: %s", err)
	}

	_, conn, err := project.ReadOrCreateProject(projectConfig, credsConfig, factory, log)
	if err != nil {
		return nil, fmt.Errorf("Error reading or creating project in Ops Manager: %s", err)
	}
	return conn, nil
}

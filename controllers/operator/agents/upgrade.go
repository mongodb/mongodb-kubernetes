package agents

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

var nextScheduledTime time.Time

const pause = time.Hour * 24

var mux sync.Mutex

func init() {
	ScheduleUpgrade()
}

// ClientSecret is a wrapper that joins a client and a secretClient.
type ClientSecret struct {
	Client       kubernetesClient.Client
	SecretClient secrets.SecretClient
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
func UpgradeAllIfNeeded(ctx context.Context, cs ClientSecret, omConnectionFactory om.ConnectionFactory, watchNamespace []string, isMulti bool) {
	mux.Lock()
	defer mux.Unlock()

	if !time.Now().After(nextScheduledTime) {
		return
	}
	log := zap.S()
	log.Info("Performing a regular upgrade of Agents for all the MongoDB resources in the cluster...")

	allMDBs, err := readAllMongoDBs(ctx, cs.Client, watchNamespace, isMulti)
	if err != nil {
		log.Errorf("Failed to read MongoDB resources to ensure Agents have the latest version: %s", err)
		return
	}

	err = doUpgrade(ctx, cs.Client, cs.SecretClient, omConnectionFactory, allMDBs)
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

type dbCommonWithNamespace struct {
	objectKey types.NamespacedName
	mdbv1.DbCommonSpec
}

func doUpgrade(ctx context.Context, cmGetter configmap.Getter, secretGetter secrets.SecretClient, factory om.ConnectionFactory, mdbs []dbCommonWithNamespace) error {
	for _, mdb := range mdbs {
		log := zap.S().With(string(mdb.ResourceType), mdb.objectKey)
		conn, err := connectToMongoDB(ctx, cmGetter, secretGetter, factory, mdb, log)
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

// readAllMongoDBs returns a list of all the MongoDB resources found in the
// `watchNamespace` list.
//
// If the `watchNamespace` contains only the "" string, the MongoDB resources
// will be searched in every Namespace of the cluster.
func readAllMongoDBs(ctx context.Context, cl kubernetesClient.Client, watchNamespace []string, isMulti bool) ([]dbCommonWithNamespace, error) {
	var namespaces []string

	// 1. Find which Namespaces to look for MongoDB resources
	if len(watchNamespace) == 1 && watchNamespace[0] == "" {
		namespaceList := corev1.NamespaceList{}
		if err := cl.List(ctx, &namespaceList); err != nil {
			return []dbCommonWithNamespace{}, err
		}
		for _, item := range namespaceList.Items {
			namespaces = append(namespaces, item.Name)
		}
	} else {
		namespaces = watchNamespace
	}

	var mdbs []dbCommonWithNamespace
	// 2. Find all MongoDBs in the namespaces
	for _, ns := range namespaces {
		if isMulti {
			mongodbList := mdbmultiv1.MongoDBMultiClusterList{}
			if err := cl.List(ctx, &mongodbList, client.InNamespace(ns)); err != nil {
				return []dbCommonWithNamespace{}, err
			}
			for _, item := range mongodbList.Items {
				mdbs = append(mdbs, dbCommonWithNamespace{
					objectKey:    item.ObjectKey(),
					DbCommonSpec: item.Spec.DbCommonSpec,
				})
			}

		} else {
			mongodbList := mdbv1.MongoDBList{}
			if err := cl.List(ctx, &mongodbList, client.InNamespace(ns)); err != nil {
				return []dbCommonWithNamespace{}, err
			}
			for _, item := range mongodbList.Items {
				mdbs = append(mdbs, dbCommonWithNamespace{
					objectKey:    item.ObjectKey(),
					DbCommonSpec: item.Spec.DbCommonSpec,
				})
			}
		}
	}
	return mdbs, nil
}

func connectToMongoDB(ctx context.Context, cmGetter configmap.Getter, secretGetter secrets.SecretClient, factory om.ConnectionFactory, mdb dbCommonWithNamespace, log *zap.SugaredLogger) (om.Connection, error) {
	projectConfig, err := project.ReadProjectConfig(ctx, cmGetter, kube.ObjectKey(mdb.objectKey.Namespace, mdb.GetProject()), mdb.objectKey.Name)
	if err != nil {
		return nil, xerrors.Errorf("error reading Project Config: %w", err)
	}
	credsConfig, err := project.ReadCredentials(ctx, secretGetter, kube.ObjectKey(mdb.objectKey.Namespace, mdb.Credentials), log)
	if err != nil {
		return nil, xerrors.Errorf("error reading Credentials secret: %w", err)
	}

	_, conn, err := project.ReadOrCreateProject(projectConfig, credsConfig, factory, log)
	if err != nil {
		return nil, xerrors.Errorf("error reading or creating project in Ops Manager: %w", err)
	}
	return conn, nil
}

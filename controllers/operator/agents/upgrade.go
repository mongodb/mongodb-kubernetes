package agents

import (
	"time"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

var nextScheduledTime time.Time

const pause = time.Hour * 24

func init() {
	ScheduleUpgrade()
}

// ClientSecret is a wrapper that joins a client and a secretClient.
type ClientSecret struct {
	Client       kubernetesClient.Client
	SecretClient secrets.SecretClient
}

func upgradeIfNeeded(conn om.Connection, key types.NamespacedName, spec mdbv1.DbCommonSpec) {
	if !time.Now().After(nextScheduledTime) {
		return
	}
	log := zap.S()
	log.Infof("Performing a regular upgrade of Agents for the %s/%s MongoDB resources in the cluster...", key.Namespace, key.Name)

	err := doUpgrade(conn, key, spec)
	if err != nil {
		log.Errorf("Failed to perform upgrade of Agents: %s", err)
	}

	log.Infof("The upgrade of Agents for the MongoDB resource %s/%s in the cluster is finished.", key.Namespace, key.Name)
	nextScheduledTime = nextScheduledTime.Add(pause)
}

func UpgradeIfNeeded(mdb *mdbv1.MongoDB, conn om.Connection) {
	if len(mdb.Spec.GetExternalMembers()) > 0 {
		// Skip automatic agent upgrades when external (VM) members are present.
		zap.S().Debugf("Skipping agent upgrade for %s: resource has external members", mdb.ObjectKey())
		return
	}
	upgradeIfNeeded(conn, mdb.ObjectKey(), mdb.Spec.DbCommonSpec)
}

func UpgradeIfNeededMC(mdb *mdbmultiv1.MongoDBMultiCluster, conn om.Connection) {
	upgradeIfNeeded(conn, mdb.ObjectKey(), mdb.Spec.DbCommonSpec)
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

func doUpgrade(conn om.Connection, key types.NamespacedName, spec mdbv1.DbCommonSpec) error {
	log := zap.S().With(string(spec.ResourceType), key)

	currentVersion := ""
	if deployment, err := conn.ReadDeployment(); err == nil {
		currentVersion = deployment.GetAgentVersion()
	}
	version, err := conn.UpgradeAgentsToLatest()
	if err != nil {
		log.Warnf("Failed to schedule Agent upgrade: %s, this could be due do ongoing Automation Config publishing in Ops Manager and will get fixed during next trial", err)
		return nil
	}
	if currentVersion != version && currentVersion != "" {
		log.Debugf("Submitted the request to Ops Manager to upgrade the agents from %s to the latest version (%s)", currentVersion, version)
	}

	return nil
}

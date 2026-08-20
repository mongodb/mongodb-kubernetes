package operator

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

// assembleSnapshot performs all reads for one planning pass: the directives on every member
// cluster, the automation config and the agent-plane facts from Ops Manager. All the
// distributed-systems difficulty lives here and in the decision execution; all logic lives in
// plan(). Read failures become view-level flags (Unreachable, Read=false) rather than errors —
// what to do about a partial world is a planning decision.
func (r *ReconcileMongoDBMultiClusterLeader) assembleSnapshot(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, term int64, specHash string, conn om.Connection, projectID string, log *zap.SugaredLogger) plannerSnapshot {
	s := plannerSnapshot{
		Now:            time.Now(),
		LeadershipTerm: term,
		Name:           mrs.Name,
		Namespace:      mrs.Namespace,
		SpecHash:       specHash,
		ProjectID:      projectID,
		ClusterDomain:  mrs.Spec.GetClusterDomain(),
		SpecViolations: decentralizedSpecViolations(mrs.Spec),
		Directives:     readDirectiveViews(ctx, r.memberClusterClientsMap, kube.ObjectKey(mrs.Namespace, mrs.Name), log),
	}
	for _, item := range mrs.Spec.ClusterSpecList {
		s.Targets = append(s.Targets, clusterTarget{
			ClusterName:    item.ClusterName,
			Members:        item.Members,
			ExternalDomain: mrs.Spec.GetExternalDomainForMemberCluster(item.ClusterName),
		})
	}

	if deployment, err := conn.ReadDeployment(); err != nil {
		log.Warnf("Failed reading the automation config from Ops Manager: %s", err)
		s.AC = acView{Read: false}
	} else {
		s.AC = acViewFromDeployment(deployment, mrs.Name)
	}

	if clusterState, err := agents.GetMongoDBClusterState(conn); err != nil {
		log.Warnf("Failed reading the cluster state from Ops Manager: %s", err)
		s.OMFacts = omFactsView{Read: false}
	} else {
		s.OMFacts = omFactsFromClusterState(clusterState)
	}
	return s
}

// readDirectiveViews reads this deployment's directive on every known cluster. NotFound and a
// failed read are different worlds: NotFound is an authoritative "no entry here", a failed read
// is absence of visibility — the allocation guard must not mint new indexes over the latter.
func readDirectiveViews(ctx context.Context, clients map[string]kubernetesClient.Client, nsName types.NamespacedName, log *zap.SugaredLogger) map[string]directiveView {
	views := make(map[string]directiveView, len(clients))
	for clusterName, memberClient := range clients {
		directive := operatorv1.MongoDBDirective{}
		if err := memberClient.Get(ctx, nsName, &directive); err != nil {
			if apiErrors.IsNotFound(err) {
				views[clusterName] = directiveView{Exists: false}
				continue
			}
			log.Warnf("Failed reading the directive on cluster %s: %s", clusterName, err)
			views[clusterName] = directiveView{Unreachable: true}
			continue
		}
		views[clusterName] = directiveView{
			Exists:     true,
			Spec:       directive.Spec,
			Status:     directive.Status,
			Generation: directive.Generation,
		}
	}
	return views
}

// acViewFromDeployment reduces the deployment to what plan() keys on: per-cluster-index process
// counts for this replica set, derived from the process-name convention "<crName>-<idx>-<pod>".
func acViewFromDeployment(deployment om.Deployment, rsName string) acView {
	view := acView{Read: true, MemberCountsByIndex: map[int]int{}}
	view.LeadershipTerm, _ = deployment.GetOperatorLeadershipTerm()
	view.SpecHash, _ = deployment.GetOperatorSpecHash()
	rs := deployment.GetReplicaSetByName(rsName)
	if rs == nil {
		return view
	}
	for _, member := range rs.Members() {
		// a replica-set member's name IS the process name (not the FQDN)
		if idx, ok := clusterIndexFromProcessName(member.Name(), rsName); ok {
			view.MemberCountsByIndex[idx]++
		}
	}
	return view
}

// clusterIndexFromProcessName parses the cluster index out of "<crName>-<clusterIdx>-<podNum>",
// anchored on the CR name prefix so CR names containing dashes or digits stay unambiguous.
func clusterIndexFromProcessName(processName, rsName string) (int, bool) {
	suffix, found := strings.CutPrefix(processName, rsName+"-")
	if !found {
		return 0, false
	}
	parts := strings.Split(suffix, "-")
	if len(parts) != 2 {
		return 0, false
	}
	idx, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		return 0, false
	}
	return idx, true
}

func omFactsFromClusterState(clusterState agents.MongoDBClusterStateInOM) omFactsView {
	view := omFactsView{Read: true, ProcessStates: make(map[string]processFactView, len(clusterState.ProcessStateMap))}
	for hostname, state := range clusterState.ProcessStateMap {
		view.ProcessStates[hostname] = processFactView{
			Registered:   !state.IsStale(),
			GoalAchieved: state.GoalVersionAchieved >= clusterState.GoalVersion,
		}
	}
	return view
}

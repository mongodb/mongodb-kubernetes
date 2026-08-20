package operator

// The materializer half of the member controller: render this cluster's slice of the
// MongoDBMultiCluster (StatefulSet, services, hostname-override ConfigMap) gated by the local
// directive, mirror what was applied, repair from the mirror while holding, and observe the
// Ops Manager facts read-only. The construction layer is the legacy multi-cluster one, reused
// verbatim; only the caller changes.

import (
	"context"
	"maps"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"

	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	mconstruct "github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/multicluster"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/create"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// MirrorState is the member-local record of what this operator last applied — the hold/repair
// memory that survives operator restarts. It lives in the `<name>-state` ConfigMap on the
// member's own cluster, owner-ref'd to the local CR copy (garbage-collected with it, by design:
// if GitOps prunes the CR, the drain record goes too — recorded limitation).
type MirrorState struct {
	// AppliedSpec is the full spec the applied resources were rendered from — enough to repair
	// them even after GitOps has delivered a newer (fence-failing) spec.
	AppliedSpec mdbmultiv1.MongoDBMultiSpec `json:"appliedSpec"`
	// AppliedSpecHash is the canonical hash of AppliedSpec, as fenced on at apply time.
	AppliedSpecHash    string `json:"appliedSpecHash"`
	AppliedMemberCount int    `json:"appliedMemberCount"`
	ClusterIndex       int    `json:"clusterIndex"`
	// IndexAllocations is copied from the directive: Spec.Mapping is json:"-", so AppliedSpec
	// alone cannot rebuild peer cluster indexes (duplicate services need them).
	IndexAllocations map[string]int `json:"indexAllocations"`
	// ProjectID is the project the applied resources point at; repair and fact observation use
	// it rather than the (possibly newer) directive's.
	ProjectID string `json:"projectID"`
}

// materializeTarget is one cluster slice to render: from the live CR + directive grant on the
// advance path, from the mirror on the repair path.
type materializeTarget struct {
	clusterName      string
	clusterIndex     int
	memberCount      int
	indexAllocations map[string]int
	projectID        string
}

type materializedFacts struct {
	stsApplied      bool
	agentRegistered bool
	inGoalState     bool
}

// act materializes the granted state on this cluster and reports the staged facts. Called with
// both fences passed: the local CR copy is exactly the spec the directive was planned from.
func (r *ReconcileMongoDBDirective) act(ctx context.Context, directive *operatorv1.MongoDBDirective, mrs *mdbmultiv1.MongoDBMultiCluster, log *zap.SugaredLogger) (reconcile.Result, error) {
	// every spec'd cluster must have an index before anything is named after one — a missing
	// entry is a leader bug; the member must never allocate
	for _, item := range mrs.Spec.ClusterSpecList {
		if _, ok := directive.Spec.IndexAllocations[item.ClusterName]; !ok {
			log.Warnf("Holding: cluster %s has no index allocation in the directive", item.ClusterName)
			return reconcile.Result{RequeueAfter: directiveHoldRetry}, nil
		}
	}
	if directive.Spec.ProjectID == "" {
		log.Debug("Holding: the directive carries no Ops Manager project id")
		if err := r.writeFacts(ctx, directive, materializedFacts{}, log); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{RequeueAfter: directiveHoldRetry}, nil
	}

	target := materializeTarget{
		clusterName:      directive.Spec.ClusterName,
		clusterIndex:     directive.Spec.ClusterIndex,
		memberCount:      directive.Spec.MemberCount,
		indexAllocations: directive.Spec.IndexAllocations,
		projectID:        directive.Spec.ProjectID,
	}
	facts, err := r.materialize(ctx, mrs, target, log)
	if err != nil {
		// best-effort facts before surfacing the error: the echo already persisted, the leader
		// must still see what did not happen
		_ = r.writeFacts(ctx, directive, facts, log)
		return reconcile.Result{}, err
	}

	mirror := MirrorState{
		AppliedSpec:        mrs.Spec,
		AppliedSpecHash:    directive.Spec.TargetSpecHash,
		AppliedMemberCount: target.memberCount,
		ClusterIndex:       target.clusterIndex,
		IndexAllocations:   target.indexAllocations,
		ProjectID:          target.projectID,
	}
	if err := NewStateStore[MirrorState](mrs, kube.BaseOwnerReference(mrs), r.localClient).WriteState(ctx, &mirror, log); err != nil {
		return reconcile.Result{}, err
	}

	if err := r.writeFacts(ctx, directive, facts, log); err != nil {
		return reconcile.Result{}, err
	}
	// the requeue is the fact-observation loop: agents register and reach goal state on their
	// own clock, and nothing watches Ops Manager
	return reconcile.Result{RequeueAfter: directiveHoldRetry}, nil
}

// holdAndRepair is the hold path behind a failed fence: never advance, but keep the last applied
// state standing. With no mirror this is a plain hold (nothing was ever applied). With a mirror,
// re-render from the mirrored spec and re-apply idempotently — that repairs drift (a deleted
// StatefulSet, a lost service) even when the local CR has already moved past the fence. Repair
// errors are logged, never returned: a hold must stay a hold.
func (r *ReconcileMongoDBDirective) holdAndRepair(ctx context.Context, directive *operatorv1.MongoDBDirective, log *zap.SugaredLogger) (reconcile.Result, error) {
	owner := &mdbmultiv1.MongoDBMultiCluster{}
	owner.Name = directive.Name
	owner.Namespace = directive.Namespace
	mirror, err := NewStateStore[MirrorState](owner, nil, r.localClient).ReadState(ctx)
	if err != nil {
		if !apiErrors.IsNotFound(err) {
			log.Warnf("Failed reading the mirror state, holding without repair: %s", err)
		}
		return reconcile.Result{RequeueAfter: directiveHoldRetry}, nil
	}

	// rebuild the applied world from the mirror — deliberately not the live CR, which is either
	// ahead of the fence or gone
	mrs := &mdbmultiv1.MongoDBMultiCluster{}
	mrs.Name = directive.Name
	mrs.Namespace = directive.Namespace
	mrs.Spec = mirror.AppliedSpec

	target := materializeTarget{
		clusterName:      directive.Spec.ClusterName,
		clusterIndex:     mirror.ClusterIndex,
		memberCount:      mirror.AppliedMemberCount,
		indexAllocations: mirror.IndexAllocations,
		projectID:        mirror.ProjectID,
	}
	facts, err := r.materialize(ctx, mrs, target, log)
	if err != nil {
		log.Warnf("Repair from the mirror failed, holding: %s", err)
	}
	if err := r.writeFacts(ctx, directive, facts, log); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{RequeueAfter: directiveHoldRetry}, nil
}

// materialize renders and applies this cluster's slice and observes the Ops Manager facts. It is
// shared by the advance and repair paths and never writes the mirror or the directive status.
func (r *ReconcileMongoDBDirective) materialize(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, target materializeTarget, log *zap.SugaredLogger) (materializedFacts, error) {
	facts := materializedFacts{}

	// a seeded Mapping turns the construction layer's ClusterNum() calls into pure lookups —
	// the member must never trigger its allocation fallback
	mrs.Spec.Mapping = maps.Clone(target.indexAllocations)

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, r.localClient, secrets.SecretClient{KubeClient: r.localClient}, mrs, log)
	if err != nil {
		return facts, err
	}

	// the agent API key is pre-provisioned on this cluster (the operator never creates projects
	// or keys); its absence means the installer contract is not met — hold observably
	apiKeySecret := corev1.Secret{}
	if err := r.localClient.Get(ctx, kube.ObjectKey(mrs.Namespace, agents.ApiKeySecretName(target.projectID)), &apiKeySecret); err != nil {
		return facts, err
	}

	// construction performs no I/O with the connection; built before the StatefulSet so pod env
	// carries the project coordinates. Read-only by contract: never PrepareOpsManagerConnection.
	conn := r.omConnectionFactory(&om.OMContext{
		BaseURL:                    projectConfig.BaseURL,
		GroupID:                    target.projectID,
		GroupName:                  projectConfig.ProjectName,
		OrgID:                      projectConfig.OrgID,
		PublicKey:                  credsConfig.PublicAPIKey,
		PrivateKey:                 credsConfig.PrivateAPIKey,
		AllowInvalidSSLCertificate: !projectConfig.SSLRequireValidMMSServerCertificates,
		CACertificate:              projectConfig.SSLMMSCAConfigMapContents,
	})

	if err := r.ensureMemberServices(ctx, mrs, target, log); err != nil {
		return facts, err
	}

	hostnameOverrideCM := getHostnameOverrideConfigMap(*mrs, target.clusterIndex, target.clusterName, target.memberCount)
	if err := configmap.CreateOrUpdate(ctx, r.localClient, hostnameOverrideCM); err != nil && !apiErrors.IsAlreadyExists(err) {
		return facts, err
	}

	stsOverride := appsv1.StatefulSetSpec{}
	if item := mrs.GetClusterSpecByName(target.clusterName); item != nil && item.StatefulSetConfiguration != nil {
		stsOverride = item.StatefulSetConfiguration.SpecWrapper.Spec
	}
	opts := mconstruct.MultiClusterReplicaSetOptions(
		mconstruct.WithClusterNum(target.clusterIndex),
		Replicas(target.memberCount),
		mconstruct.WithStsOverride(&stsOverride),
		mconstruct.WithServiceName(mrs.MultiHeadlessServiceName(target.clusterIndex)),
		PodEnvVars(newPodVars(conn, projectConfig, mrs.Spec.LogLevel)),
		CurrentAgentAuthMechanism(""),
		WithLabels(mrs.GetOwnerLabels()),
		WithAdditionalMongodConfig(mrs.Spec.GetAdditionalMongodConfig()),
		WithInitDatabaseNonStaticImage(images.ContainerImage(r.imageUrls, util.InitDatabaseImageUrlEnv, r.initDatabaseNonStaticImageVersion)),
		WithDatabaseNonStaticImage(images.ContainerImage(r.imageUrls, util.NonStaticDatabaseEnterpriseImage, r.databaseNonStaticImageVersion)),
		// static architecture needs an agent version from OM (a read the POC skips); non-static
		// runs with an unversioned agent image reference, like the legacy non-static path
		WithAgentImage(images.ContainerImage(r.imageUrls, util.AgentImageUrlEnv, "")),
		WithMongodbImage(images.GetOfficialImage(r.imageUrls, mrs.Spec.Version, mrs.GetAnnotations(), r.defaultArchitecture)),
		WithDefaultArchitecture(r.defaultArchitecture),
	)
	sts := mconstruct.MultiClusterStatefulSet(*mrs, opts)
	if _, err := statefulset.CreateOrUpdateStatefulset(ctx, r.localClient, mrs.Namespace, log, &sts); err != nil {
		return facts, err
	}
	facts.stsApplied = true

	facts.agentRegistered, facts.inGoalState = r.observeAgentFacts(conn, mrs, target, log)
	return facts, nil
}

// ensureMemberServices creates on THIS cluster what the legacy reconcileServices creates per
// member cluster: the SRV and headless services, own pod services at the granted count, and —
// honoring spec.duplicateServiceObjects (nil means on) — the peer clusters' pod services for
// cross-cluster resolution. Peer counts come from the spec (the fences guarantee it is the plan
// this directive was cut from); peer indexes from the directive's allocation map.
func (r *ReconcileMongoDBDirective) ensureMemberServices(ctx context.Context, mrs *mdbmultiv1.MongoDBMultiCluster, target materializeTarget, log *zap.SugaredLogger) error {
	if target.memberCount > 0 {
		if err := ensureSRVService(ctx, r.localClient, getSRVService(mrs), target.clusterName); err != nil {
			return err
		}

		headlessServiceName := mrs.MultiHeadlessServiceName(target.clusterIndex)
		headlessService := create.BuildService(kube.ObjectKey(mrs.Namespace, headlessServiceName), mrs, ptr.To(headlessServiceName), nil, mrs.Spec.AdditionalMongodConfig.GetPortOrDefault(), omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
		headlessService.OwnerReferences = nil
		if err := ensureHeadlessService(ctx, r.localClient, headlessService, target.clusterName); err != nil {
			return err
		}
	}

	shouldCreateDuplicates := mrs.Spec.DuplicateServiceObjects == nil || *mrs.Spec.DuplicateServiceObjects
	for _, item := range mrs.Spec.ClusterSpecList {
		if !shouldCreateDuplicates && item.ClusterName != target.clusterName {
			continue
		}
		if item.ClusterName == target.clusterName {
			// own pods follow the grant, not the spec goal
			item.Members = target.memberCount
		}
		if err := ensureServices(ctx, r.localClient, target.clusterName, mrs, item, log); err != nil {
			return err
		}
	}
	return nil
}

// observeAgentFacts reads the agent facts from Ops Manager, once, non-blocking. Exact hostname
// equality against the process state map — never the prefix-matching registration check.
func (r *ReconcileMongoDBDirective) observeAgentFacts(conn om.Connection, mrs *mdbmultiv1.MongoDBMultiCluster, target materializeTarget, log *zap.SugaredLogger) (agentRegistered, inGoalState bool) {
	clusterState, err := agents.GetMongoDBClusterState(conn)
	if err != nil {
		log.Warnf("Failed reading the cluster state from Ops Manager: %s", err)
		return false, false
	}

	hostnames := dns.GetMultiClusterProcessHostnames(mrs.Name, mrs.Namespace, target.clusterIndex, target.memberCount, mrs.Spec.GetClusterDomain(), mrs.Spec.GetExternalDomainForMemberCluster(target.clusterName))
	agentRegistered, inGoalState = true, true
	for _, hostname := range hostnames {
		processState, ok := clusterState.ProcessStateMap[hostname]
		if !ok || processState.IsStale() {
			agentRegistered = false
		}
		if !ok || processState.GoalVersionAchieved < clusterState.GoalVersion {
			inGoalState = false
		}
	}
	return agentRegistered, inGoalState
}

// writeFacts reports the staged facts in the directive status. Equality-guarded like the echo,
// so the unfiltered directive watch quiesces on its own status writes.
func (r *ReconcileMongoDBDirective) writeFacts(ctx context.Context, directive *operatorv1.MongoDBDirective, facts materializedFacts, log *zap.SugaredLogger) error {
	if directive.Status.StsApplied == facts.stsApplied && directive.Status.AgentRegistered == facts.agentRegistered && directive.Status.InGoalState == facts.inGoalState {
		return nil
	}
	directive.Status.StsApplied = facts.stsApplied
	directive.Status.AgentRegistered = facts.agentRegistered
	directive.Status.InGoalState = facts.inGoalState
	log.Debugf("Reporting facts: stsApplied=%t agentRegistered=%t inGoalState=%t", facts.stsApplied, facts.agentRegistered, facts.inGoalState)
	return r.localClient.Status().Update(ctx, directive)
}

package create

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status/pvc"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/placeholders"
	enterprisests "github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

var (
	externalConnectivityPortName = "external-connectivity-port"
	internalConnectivityPortName = "internal-connectivity-port"
	backupPortName               = "backup-port"
	appLabelKey                  = "app"
)

const agentHostnameOverrideEnv = "MDB_AGENT_HOSTNAME_OVERRIDE"

// DatabaseInKubernetes creates (updates if it exists) the StatefulSet with its Services.
// It returns any errors coming from Kubernetes API.
func DatabaseInKubernetes(ctx context.Context, client kubernetesClient.Client, secretClient secrets.SecretClient, mdb mdbv1.MongoDB, sts appsv1.StatefulSet, config func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) (*appsv1.StatefulSet, error) {
	opts := config(mdb)
	sts = *sts.DeepCopy()
	if err := ReconcileDatabaseAgentSettings(ctx, client, secretClient, &sts, opts); err != nil {
		return nil, err
	}
	set, err := enterprisests.CreateOrUpdateStatefulset(ctx, client, mdb.Namespace, log, &sts)
	if err != nil {
		return nil, err
	}

	// Everything below - the internal service and the per-pod external services - is the single-cluster path.
	// A multi-cluster sharded cluster has its services created by ShardedClusterReconcileHelper.reconcileServices
	if mdb.Spec.IsMultiCluster() && mdb.IsShardedCluster() {
		return set, nil
	}

	namespacedName := kube.ObjectKey(mdb.Namespace, set.Spec.ServiceName)
	internalService := BuildService(namespacedName, &mdb, &set.Spec.ServiceName, nil, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	internalService.OwnerReferences = mdb.OwnerReferenceForMemberCluster()

	// Adds Prometheus Port if Prometheus has been enabled.
	prom := mdb.GetPrometheus()
	if prom != nil {
		//nolint:gosec // suppressing integer overflow warning for int32(prom.GetPort())
		internalService.Spec.Ports = append(internalService.Spec.Ports, corev1.ServicePort{Port: int32(prom.GetPort()), Name: "prometheus"})
	}

	if err := service.CreateOrUpdateService(ctx, client, internalService); err != nil {
		return set, err
	}

	// opts.Replicas, not mdb.GetSpec().Replicas(): for a sharded cluster the latter returns
	// MongosCount regardless of tier, which would give config servers and shards the wrong number of
	// services. opts.Replicas is this tier's count for this reconciliation.
	for podNum := 0; podNum < opts.Replicas; podNum++ {
		namespacedName = kube.ObjectKey(mdb.Namespace, dns.GetExternalServiceName(set.Name, podNum))
		if !needsExternalServices(mdb, opts) {
			if err := service.DeleteServiceIfItExists(ctx, client, namespacedName); err != nil {
				return set, err
			}
		} else {
			if err := createExternalServices(ctx, client, mdb, opts, namespacedName, set, podNum, log); err != nil {
				return set, err
			}
		}
	}

	return set, nil
}

// ReconcileDatabaseAgentSettings renders the native agent settings, reconciles the settings
// ConfigMap and credential, and supplies the optional custom hostname to the launcher.
func ReconcileDatabaseAgentSettings(ctx context.Context, client kubernetesClient.Client, secretClient secrets.SecretClient, sts *appsv1.StatefulSet, opts construct.DatabaseStatefulSetOptions) error {
	cm, err := construct.DatabaseAgentSettingsConfigMap(*sts, opts)
	if err != nil {
		return fmt.Errorf("cannot render agent settings: %w", err)
	}
	agent := construct.AgentContainer(*sts)
	if secretClient.VaultClient == nil && opts.PodVars != nil && opts.PodVars.ProjectID != "" && construct.AgentAPIKeySecretName(*sts) == "" {
		// Member clusters have no Vault client. The global Vault backend omits the Kubernetes key
		// volume in the shared construct, so mount the member cluster Secret explicitly.
		podtemplatespec.WithVolume(enterprisests.CreateVolumeFromSecret(construct.AgentAPIKeyVolumeName, agents.ApiKeySecretName(opts.PodVars.ProjectID)))(&sts.Spec.Template)
		container.WithVolumeMounts([]corev1.VolumeMount{enterprisests.CreateVolumeMount(construct.AgentAPIKeyVolumeName, construct.AgentAPIKeySecretPath)})(agent)
	}
	var parameters mdbv1.StartupParameters
	if opts.AgentConfig != nil {
		parameters = construct.AgentSettingsOverrides(opts.AgentConfig.StartupParameters)
	}
	baseURL := env.ToMap(agent.Env...)[util.EnvVarBaseUrl]
	needsCredential := baseURL != "" && !construct.AgentEnvironmentIsRuntime(*agent, util.EnvVarBaseUrl)
	if override, ok := parameters["mmsBaseUrl"]; ok {
		needsCredential = override != ""
	}
	var basePath string
	if secretClient.VaultClient != nil {
		basePath = secretClient.VaultClient.DatabaseSecretPath()
	}
	if err := reconcileAgentSettings(ctx, client, secretClient, sts, cm, parameters, basePath, needsCredential); err != nil {
		return err
	}
	container.WithArgs(nil)(agent)
	agent.Env = slices.DeleteFunc(agent.Env, func(variable corev1.EnvVar) bool { return variable.Name == agentHostnameOverrideEnv })
	if hostname, ok := parameters["overrideLocalHost"]; ok {
		// Kubernetes expands references using preceding environment entries. Append after all pod-template merges.
		agent.Env = append(agent.Env, corev1.EnvVar{Name: agentHostnameOverrideEnv, Value: hostname})
	}
	return nil
}

func reconcileAgentSettings(ctx context.Context, client kubernetesClient.Client, secretClient secrets.SecretClient, sts *appsv1.StatefulSet, cm corev1.ConfigMap, parameters mdbv1.StartupParameters, basePath string, needsCredential bool) error {
	agent := construct.AgentContainer(*sts)
	if agent == nil {
		return fmt.Errorf("agent container not found in StatefulSet %s", sts.Name)
	}
	credential, err := reconcileAgentCredential(ctx, secretClient, sts, agent, parameters, basePath, needsCredential, cm.Data[construct.AgentSettingsFileName])
	if err != nil {
		return err
	}
	return writeAgentSettings(ctx, client, sts, agent, cm, credential)
}

// reconcileAgentCredential delivers the startup credential and returns the value covered by the
// rollout digest. The credential comes from an explicit override, the existing Secret, or a
// best-effort read when the rendered settings will read the key at startup.
func reconcileAgentCredential(ctx context.Context, secretClient secrets.SecretClient, sts *appsv1.StatefulSet, agent *corev1.Container, parameters mdbv1.StartupParameters, basePath string, needsCredential bool, settings string) (string, error) {
	if override, ok := parameters["mmsApiKey"]; ok && parameters["mmsApiKeyExec"] == "" {
		keySecret := secret.Builder().SetName(sts.Name+"-agent-api-key").SetNamespace(sts.Namespace).
			SetOwnerReferences(sts.OwnerReferences).SetLabels(sts.Labels).SetField(util.OmAgentApiKey, override).Build()
		if err := secretClient.PutSecretIfChanged(ctx, keySecret, basePath); err != nil {
			return "", fmt.Errorf("cannot reconcile agent credential override: %w", err)
		}
		if vault.IsVaultSecretBackend() {
			secretPath := fmt.Sprintf("%s/%s/%s", basePath, sts.Namespace, keySecret.Name)
			sts.Spec.Template.Annotations = merge.StringToStringMap(sts.Spec.Template.Annotations, map[string]string{
				"vault.hashicorp.com/agent-inject-secret-agentApiKey":   secretPath,
				"vault.hashicorp.com/agent-inject-template-agentApiKey": fmt.Sprintf(vault.DEFAULT_AGENT_INJECT_TEMPLATE, secretPath, util.OmAgentApiKey),
				"vault.hashicorp.com/secret-volume-path-agentApiKey":    construct.AgentAPIKeySecretPath,
			})
		} else {
			upsertAgentAPIKeyVolume(sts, agent, keySecret)
		}
		return override, nil
	}
	if parameters["mmsApiKeyExec"] == "" && needsCredential {
		credential, err := databaseAgentCredential(ctx, secretClient, *sts)
		if err != nil {
			return "", fmt.Errorf("cannot read agent startup credential: %w", err)
		}
		return credential, nil
	}
	if parameters["mmsApiKeyExec"] == "" && settingsConsumeCredential(settings) {
		// Digest coverage only. A missing Secret must not fail reconciliation: the agent reports
		// that itself. A transient read error must fail reconciliation, because a credential-free
		// digest would change the template and roll the pods a second time on recovery.
		value, err := databaseAgentCredential(ctx, secretClient, *sts)
		if err != nil {
			if apiErrors.IsNotFound(err) {
				return "", nil
			}
			return "", err
		}
		return value, nil
	}
	return "", nil
}

// upsertAgentAPIKeyVolume replaces the projected API-key volume in place and keeps the existing
// item mode. WithVolume merges item lists, so it would keep stale keys that the generated Secret
// does not contain. replaceVolume replaces the whole volume instead.
func upsertAgentAPIKeyVolume(sts *appsv1.StatefulSet, agent *corev1.Container, keySecret corev1.Secret) {
	keyVolume := enterprisests.CreateVolumeFromSecret(construct.AgentAPIKeyVolumeName, keySecret.Name)
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Name != construct.AgentAPIKeyVolumeName || volume.Secret == nil {
			continue
		}
		keyVolume = *volume.DeepCopy()
		keyVolume.Secret.SecretName = keySecret.Name
		if len(volume.Secret.Items) > 0 {
			// The generated Secret contains only the override credential.
			item := corev1.KeyToPath{Key: util.OmAgentApiKey, Path: util.OmAgentApiKey}
			for _, existing := range volume.Secret.Items {
				if existing.Path == item.Path {
					item.Mode = existing.Mode
					break
				}
			}
			keyVolume.Secret.Items = []corev1.KeyToPath{item}
		}
		break
	}
	replaceVolume(&sts.Spec.Template, keyVolume)
	if !slices.ContainsFunc(agent.VolumeMounts, func(mount corev1.VolumeMount) bool { return mount.Name == construct.AgentAPIKeyVolumeName }) {
		container.WithVolumeMounts([]corev1.VolumeMount{enterprisests.CreateVolumeMount(construct.AgentAPIKeyVolumeName, construct.AgentAPIKeySecretPath, enterprisests.WithReadOnly(true))})(agent)
	}
}

// replaceVolume replaces a volume by name without changing its position, or appends it.
func replaceVolume(template *corev1.PodTemplateSpec, volume corev1.Volume) {
	for i := range template.Spec.Volumes {
		if template.Spec.Volumes[i].Name == volume.Name {
			template.Spec.Volumes[i] = volume
			return
		}
	}
	template.Spec.Volumes = append(template.Spec.Volumes, volume)
}

func writeAgentSettings(ctx context.Context, client kubernetesClient.Client, sts *appsv1.StatefulSet, agent *corev1.Container, cm corev1.ConfigMap, credential string) error {
	if err := configmap.CreateOrUpdate(ctx, client, cm); err != nil {
		return fmt.Errorf("cannot reconcile agent settings ConfigMap: %w", err)
	}
	podtemplatespec.WithVolume(enterprisests.CreateVolumeFromConfigMap(construct.AgentSettingsVolume, cm.Name))(&sts.Spec.Template)
	container.WithVolumeMounts([]corev1.VolumeMount{enterprisests.CreateVolumeMount(construct.AgentSettingsVolume, construct.AgentSettingsPath, enterprisests.WithReadOnly(true))})(agent)
	digest := sha256.Sum256([]byte(cm.Data[construct.AgentSettingsFileName] + "\x00" + credential))
	sts.Spec.Template.Annotations = merge.StringToStringMap(sts.Spec.Template.Annotations, map[string]string{construct.AgentSettingsHash: fmt.Sprintf("%x", digest)})
	return nil
}

// settingsConsumeCredential reports whether the rendered settings will make the agent read the API
// key at startup. When they do, the startup digest must cover the credential. The operator cannot
// resolve a runtime BASE_URL at reconcile time, so needsCredential alone is false for those
// deployments. Hashing the credential only when needsCredential is true would then miss key
// rotation and leave pods running with a revoked key until something else restarted them.
func settingsConsumeCredential(settings string) bool {
	return strings.Contains("\n"+settings, "\nmmsApiKeyExec=")
}

func databaseAgentCredential(ctx context.Context, secretClient secrets.SecretClient, sts appsv1.StatefulSet) (string, error) {
	// Member cluster secret clients carry no Vault client because Vault is not supported for
	// multi-cluster databases. Fall through to the Kubernetes Secret mounted in the member cluster.
	if vault.IsVaultSecretBackend() && secretClient.VaultClient != nil {
		data, err := secretClient.VaultClient.ReadSecretString(sts.Spec.Template.Annotations["vault.hashicorp.com/agent-inject-secret-agentApiKey"])
		if err != nil {
			return "", err
		}
		credential, ok := data[util.OmAgentApiKey]
		if !ok {
			return "", fmt.Errorf("agent credential secret does not contain key %s", util.OmAgentApiKey)
		}
		return credential, nil
	}
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Name != construct.AgentAPIKeyVolumeName || volume.Secret == nil {
			continue
		}
		key := util.OmAgentApiKey
		if len(volume.Secret.Items) > 0 {
			key = ""
			for _, item := range volume.Secret.Items {
				if item.Path == util.OmAgentApiKey {
					key = item.Key
				}
			}
			if key == "" {
				return "", fmt.Errorf("agent credential volume does not project %s", util.OmAgentApiKey)
			}
		}
		// The member cluster Secret is created immediately before this read. The member client
		// is cached, so the cache can lag the create. Allow up to 2s for a loaded cache to
		// converge, and still fail promptly on a Secret that is truly missing.
		var data map[string]string
		var err error
		for range 10 {
			data, err = secret.ReadStringData(ctx, secretClient.KubeClient, kube.ObjectKey(sts.Namespace, volume.Secret.SecretName))
			if err == nil || !apiErrors.IsNotFound(err) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			return "", err
		}
		value, ok := data[key]
		if !ok {
			return "", fmt.Errorf("agent credential secret %s does not contain key %s", volume.Secret.SecretName, key)
		}
		return strings.TrimSuffix(value, "\n"), nil
	}
	return "", fmt.Errorf("agent credential volume not found in StatefulSet %s", sts.Name)
}

// needsExternalServices reports whether this tier wants per-pod external services in a single-cluster
// deployment.
//
// Config servers and shards only need external services when an external domain is configured, because
// that is the only thing that puts their external names into the automation config - this mirrors the
// rule reconcileConfigServerServices and reconcileShardServices apply in multi-cluster. Mongos and the
// non-sharded resource types always get an external service regardless of externalDomain.
func needsExternalServices(mdb mdbv1.MongoDB, opts construct.DatabaseStatefulSetOptions) bool {
	if opts.ExternalAccessConfiguration == nil {
		return false
	}

	if mdb.IsShardedCluster() && !opts.IsMongos() {
		return opts.ExternalAccessConfiguration.ExternalDomain != nil
	}

	return true
}

// HandlePVCResize handles the state machine of a PVC resize.
// Note: it modifies the desiredSTS.annotation to trigger a rolling restart later
// We leverage workflowStatus.WithAdditionalOptions(...) to merge/update/add to existing mdb.status.pvc
func HandlePVCResize(ctx context.Context, memberClient kubernetesClient.Client, desiredSts *appsv1.StatefulSet, log *zap.SugaredLogger) workflow.Status {
	existingStatefulSet, stsErr := memberClient.GetStatefulSet(ctx, kube.ObjectKey(desiredSts.Namespace, desiredSts.Name))
	if stsErr != nil {
		// if we are here it means its first reconciling, we can skip the whole pvc state machine
		if apiErrors.IsNotFound(stsErr) {
			return workflow.OK()
		} else {
			return workflow.Failed(stsErr)
		}
	}

	pvcResizes := resourceStorageHasChanged(existingStatefulSet.Spec.VolumeClaimTemplates, desiredSts.Spec.VolumeClaimTemplates)

	increaseStorageOfAtLeastOnePVC := false
	// we have decreased the storage for at least one pvc, we do not support that
	for _, pvcResize := range pvcResizes {
		if pvcResize.resizeIndicator == 1 {
			log.Debug("Can't update the stateful set, as we cannot decrease the pvc size")
			return workflow.Failed(xerrors.Errorf("can't update pvc and statefulset to a smaller storage, from: %s - to:%s", pvcResize.from, pvcResize.to))
		}
		if pvcResize.resizeIndicator == -1 {
			log.Infof("Detected PVC size expansion; for pvc %s, from: %s to: %s", pvcResize.pvcName, pvcResize.from, pvcResize.to)
			increaseStorageOfAtLeastOnePVC = true
		}
	}

	// The sts claim has been increased (based on resourceChangeIndicator) for at least one PVC,
	// and we are not in the middle of a resize (that means pvcPhase is pvc.PhaseNoAction) for this statefulset.
	// This means we want to start one
	if increaseStorageOfAtLeastOnePVC {
		if err := enterprisests.AddPVCAnnotation(desiredSts); err != nil {
			return workflow.Failed(xerrors.Errorf("can't add pvc annotation, err: %s", err))
		}

		log.Infof("Detected PVC size expansion; patching all pvcs and increasing the size for sts: %s", desiredSts.Name)
		if err := resizePVCsStorage(ctx, memberClient, desiredSts, log); err != nil {
			return workflow.Failed(xerrors.Errorf("can't resize pvc, err: %s", err))
		}

		finishedResizing, err := hasFinishedResizing(ctx, memberClient, desiredSts)
		if err != nil {
			return workflow.Failed(err)
		}

		if finishedResizing {
			log.Info("PVCs finished resizing")
			log.Info("Deleting StatefulSet and orphan pods")
			// Cascade delete the StatefulSet
			deletePolicy := metav1.DeletePropagationOrphan
			if err := memberClient.Delete(ctx, desiredSts, client.PropagationPolicy(deletePolicy)); err != nil && !apiErrors.IsNotFound(err) {
				return workflow.Failed(xerrors.Errorf("error deleting sts, err: %s", err))
			}

			deletedIsStatefulset := checkStatefulsetIsDeleted(ctx, memberClient, desiredSts, 1*time.Second, log)

			if !deletedIsStatefulset {
				log.Info("deletion has not been reflected in kube yet, restarting the reconcile")
				return workflow.Pending("STS has been orphaned but not yet reflected in kubernetes. " +
					"Restarting the reconcile").WithAdditionalOptions(status.NewPVCsStatusOption(&status.PVC{Phase: pvc.PhasePVCResize, StatefulsetName: desiredSts.Name}))
			}
			log.Info("Statefulset have been orphaned")
			return workflow.OK().WithAdditionalOptions(status.NewPVCsStatusOption(&status.PVC{Phase: pvc.PhaseSTSOrphaned, StatefulsetName: desiredSts.Name}))
		} else {
			log.Info("PVCs are still resizing, waiting until it has finished")
			return workflow.Pending("PVC resizes has not finished; current state of sts: %s: %s", desiredSts.Name, pvc.PhasePVCResize).WithAdditionalOptions(status.NewPVCsStatusOption(&status.PVC{Phase: pvc.PhasePVCResize, StatefulsetName: desiredSts.Name}))
		}
	}

	return workflow.OK()
}

// preserveExistingVolumeClaimTemplateMetadata copies metadata.labels and metadata.annotations
// from the live StatefulSet's volumeClaimTemplates onto the desired ones, matching by claim name.

// spec.volumeClaimTemplates is immutable in Kubernetes, so any divergence - adding, changing or
// removing a label - makes the whole StatefulSet update fail (CLOUDP-208587).
//
// Divergences are dropped rather than failing the reconcile, and reported when a statefulSet
// override explicitly asks for metadata which cannot be applied.
func preserveExistingVolumeClaimTemplateMetadata(ctx context.Context, memberClient kubernetesClient.Client, desiredSts *appsv1.StatefulSet, log *zap.SugaredLogger) error {
	existingStatefulSet, err := memberClient.GetStatefulSet(ctx, kube.ObjectKey(desiredSts.Namespace, desiredSts.Name))
	if err != nil {
		// First reconciliation, the desired volume claim templates are used as they are.
		if apiErrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	for _, existingClaim := range existingStatefulSet.Spec.VolumeClaimTemplates {
		for i := range desiredSts.Spec.VolumeClaimTemplates {
			desiredClaim := &desiredSts.Spec.VolumeClaimTemplates[i]
			if desiredClaim.Name != existingClaim.Name {
				continue
			}

			warnAboutIgnoredVolumeClaimTemplateMetadata(desiredSts.Name, *desiredClaim, existingClaim, log)

			desiredClaim.Labels = existingClaim.Labels
			desiredClaim.Annotations = existingClaim.Annotations
		}
	}

	return nil
}

// warnAboutIgnoredVolumeClaimTemplateMetadata reports the labels and annotations which a statefulSet
// override asks for but which cannot be applied to an already existing volume claim template.
func warnAboutIgnoredVolumeClaimTemplateMetadata(stsName string, desiredClaim, existingClaim corev1.PersistentVolumeClaim, log *zap.SugaredLogger) {
	if droppedLabels := droppedMetadataEntries(desiredClaim.Labels, existingClaim.Labels); len(droppedLabels) > 0 {
		log.Warnf("Not applying the labels %v to the volume claim template %s of statefulset %s: spec.volumeClaimTemplates "+
			"is immutable, so the existing labels %v are kept instead", droppedLabels, existingClaim.Name, stsName, existingClaim.Labels)
	}

	if droppedAnnotations := droppedMetadataEntries(desiredClaim.Annotations, existingClaim.Annotations); len(droppedAnnotations) > 0 {
		log.Warnf("Not applying the annotations %v to the volume claim template %s of statefulset %s: spec.volumeClaimTemplates "+
			"is immutable, so the existing annotations %v are kept instead", droppedAnnotations, existingClaim.Name, stsName, existingClaim.Annotations)
	}
}

// droppedMetadataEntries returns the desired metadata entries which an update would have to change.
//
// Entries only present in the existing map are deliberately left out in order to not trigger a warning
// on every single reconciliation.
func droppedMetadataEntries(desired, existing map[string]string) map[string]string {
	dropped := map[string]string{}
	for key, desiredValue := range desired {
		if existingValue, ok := existing[key]; !ok || existingValue != desiredValue {
			dropped[key] = desiredValue
		}
	}

	return dropped
}

func checkStatefulsetIsDeleted(ctx context.Context, memberClient kubernetesClient.Client, desiredSts *appsv1.StatefulSet, sleepDuration time.Duration, log *zap.SugaredLogger) bool {
	// After deleting the statefulset it can take seconds to be reflected in kubernetes.
	// In case it is still not reflected
	deletedIsStatefulset := false
	for range 3 {
		time.Sleep(sleepDuration)
		_, stsErr := memberClient.GetStatefulSet(ctx, kube.ObjectKey(desiredSts.Namespace, desiredSts.Name))
		if apiErrors.IsNotFound(stsErr) {
			deletedIsStatefulset = true
			break
		} else {
			log.Info("Statefulset still exists, attempting again")
		}
	}
	return deletedIsStatefulset
}

func hasFinishedResizing(ctx context.Context, memberClient kubernetesClient.Client, desiredSts *appsv1.StatefulSet) (bool, error) {
	pvcList := corev1.PersistentVolumeClaimList{}
	if err := memberClient.List(ctx, &pvcList, client.InNamespace(desiredSts.Namespace)); err != nil {
		return false, err
	}

	finishedResizing := true
	for _, currentPVC := range pvcList.Items {
		if template, index := getMatchingPVCTemplateFromSTS(desiredSts, &currentPVC); template != nil {
			if currentPVC.Status.Capacity.Storage().Cmp(*desiredSts.Spec.VolumeClaimTemplates[index].Spec.Resources.Requests.Storage()) != 0 {
				finishedResizing = false
			}
		}
	}
	return finishedResizing, nil
}

// resizePVCsStorage takes the sts we want to create and update all matching pvc with the new storage
func resizePVCsStorage(ctx context.Context, kubeClient kubernetesClient.Client, statefulSetToCreate *appsv1.StatefulSet, log *zap.SugaredLogger) error {
	// this is to ensure that requests to a potentially not allowed resource is not blocking the operator until the end
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	pvcList := corev1.PersistentVolumeClaimList{}
	if err := kubeClient.List(ctx, &pvcList, client.InNamespace(statefulSetToCreate.Namespace)); err != nil {
		return err
	}

	for _, existingPVC := range pvcList.Items {
		if template, _ := getMatchingPVCTemplateFromSTS(statefulSetToCreate, &existingPVC); template != nil {
			currentSize := existingPVC.Spec.Resources.Requests[corev1.ResourceStorage]
			targetSize := *template.Spec.Resources.Requests.Storage()
			log.Infof("Resizing PVC %s/%s from %s to %s", existingPVC.GetNamespace(), existingPVC.GetName(), currentSize.String(), targetSize.String())
			existingPVC.Spec.Resources.Requests[corev1.ResourceStorage] = targetSize
			if err := kubeClient.Update(ctx, &existingPVC); err != nil {
				return err
			}
		}
	}
	return nil
}

func getMatchingPVCTemplateFromSTS(statefulSet *appsv1.StatefulSet, pvc *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, int) {
	for i, claimTemplate := range statefulSet.Spec.VolumeClaimTemplates {
		expectedPrefix := fmt.Sprintf("%s-%s", claimTemplate.Name, statefulSet.Name)

		// Regex to match expectedPrefix followed by a dash and a number (ordinal)
		regexPattern := fmt.Sprintf("^%s-[0-9]+$", regexp.QuoteMeta(expectedPrefix))
		if matched, _ := regexp.MatchString(regexPattern, pvc.Name); matched {
			return &claimTemplate, i
		}
	}
	return nil, -1
}

// createExternalServices creates the per-pod external LoadBalancer services for one StatefulSet.
// The tier's effective external access configuration arrives on opts.ExternalAccessConfiguration; the
// caller guarantees it is non-nil.
func createExternalServices(ctx context.Context, client kubernetesClient.Client, mdb mdbv1.MongoDB, opts construct.DatabaseStatefulSetOptions, namespacedName client.ObjectKey, set *appsv1.StatefulSet, podNum int, log *zap.SugaredLogger) error {
	externalAccess := opts.ExternalAccessConfiguration

	// TODO: we should not use OpsManager specific type `omv1.MongoDBOpsManagerServiceDefinition`
	externalService := BuildService(namespacedName, &mdb, &set.Spec.ServiceName, new(dns.GetPodName(set.Name, podNum)), opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeLoadBalancer})
	externalService.OwnerReferences = mdb.OwnerReferenceForMemberCluster()

	if externalAccess.ExternalDomain != nil {
		// When an external domain is defined, we put it into process.hostname in automation config. Because of that we need to define additional well-defined port for backups.
		// This backup port is not needed when we use headless service, because then agent is resolving DNS directly to pod's IP and that allows to connect
		// to any port in a pod, even ephemeral one.
		// When we put any other address than headless service into process.hostname: non-headless service fqdn (e.g. in multi cluster using service mesh) or
		// external domain (e.g. for multi-cluster no-mesh), then we need to define backup port.
		// In the agent process, we pass -ephemeralPortOffset 1 argument to define, that backup port should be a standard port+1.
		backupPort := GetNonEphemeralBackupPort(opts.ServicePort)
		externalService.Spec.Ports = append(externalService.Spec.Ports, corev1.ServicePort{Port: backupPort, TargetPort: intstr.FromInt32(backupPort), Name: "backup"})
	}

	if externalAccess.ExternalService.SpecWrapper != nil {
		externalService.Spec = merge.ServiceSpec(externalService.Spec, externalAccess.ExternalService.SpecWrapper.Spec)
	}
	externalService.Annotations = merge.StringToStringMap(externalService.Annotations, externalAccess.ExternalService.Annotations)

	placeholderReplacer := GetSingleClusterMongoDBPlaceholderReplacer(mdb.Name, set.Name, mdb.Namespace, mdb.ServiceName(), externalAccess.ExternalDomain, mdb.Spec.GetClusterDomain(), podNum, mdb.GetResourceType())
	if processedAnnotations, replacedFlag, err := placeholderReplacer.ProcessMap(externalService.Annotations); err != nil {
		return xerrors.Errorf("failed to process annotations in service %s: %w", externalService.Name, err)
	} else if replacedFlag {
		log.Debugf("Replaced placeholders in annotations in external service: %s. Annotations before: %+v, annotations after: %+v", externalService.Name, externalService.Annotations, processedAnnotations)
		externalService.Annotations = processedAnnotations
	}

	err := service.CreateOrUpdateService(ctx, client, externalService)
	if err != nil && !apiErrors.IsAlreadyExists(err) {
		return xerrors.Errorf("failed to created external service: %s, err: %w", externalService.Name, err)
	}
	return nil
}

const (
	PlaceholderPodIndex            = "podIndex"
	PlaceholderNamespace           = "namespace"
	PlaceholderResourceName        = "resourceName"
	PlaceholderPodName             = "podName"
	PlaceholderStatefulSetName     = "statefulSetName"
	PlaceholderExternalServiceName = "externalServiceName"
	PlaceholderMongosProcessDomain = "mongosProcessDomain"
	PlaceholderMongodProcessDomain = "mongodProcessDomain"
	PlaceholderMongosProcessFQDN   = "mongosProcessFQDN"
	PlaceholderMongodProcessFQDN   = "mongodProcessFQDN"
	PlaceholderClusterName         = "clusterName"
	PlaceholderClusterIndex        = "clusterIndex"
)

func GetSingleClusterMongoDBPlaceholderReplacer(resourceName string, statefulSetName string, namespace string, serviceName string, externalDomain *string, clusterDomain string, podIdx int, resourceType mdbv1.ResourceType) *placeholders.Replacer {
	podName := dns.GetPodName(statefulSetName, podIdx)
	placeholderValues := map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIdx),
		PlaceholderNamespace:           namespace,
		PlaceholderResourceName:        resourceName,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     statefulSetName,
		PlaceholderExternalServiceName: dns.GetExternalServiceName(statefulSetName, podIdx),
	}

	if resourceType == mdbv1.ShardedCluster {
		placeholderValues[PlaceholderMongosProcessDomain] = dns.GetServiceFQDN(serviceName, namespace, clusterDomain)
		placeholderValues[PlaceholderMongosProcessFQDN] = dns.GetPodFQDN(podName, serviceName, namespace, clusterDomain, externalDomain)
		if externalDomain != nil {
			placeholderValues[PlaceholderMongosProcessDomain] = *externalDomain
		}
	} else {
		placeholderValues[PlaceholderMongodProcessDomain] = dns.GetServiceFQDN(serviceName, namespace, clusterDomain)
		placeholderValues[PlaceholderMongodProcessFQDN] = dns.GetPodFQDN(podName, serviceName, namespace, clusterDomain, externalDomain)
		if externalDomain != nil {
			placeholderValues[PlaceholderMongodProcessDomain] = *externalDomain
		}
	}

	return placeholders.New(placeholderValues)
}

func GetMultiClusterMongoDBPlaceholderReplacer(name string, stsName string, namespace string, clusterName string, clusterNum int, externalDomain *string, clusterDomain string, podIdx int) *placeholders.Replacer {
	podName := dns.GetMultiPodName(stsName, clusterNum, podIdx)
	serviceDomain := dns.GetServiceDomain(namespace, clusterDomain, externalDomain)
	placeholderValues := map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIdx),
		PlaceholderNamespace:           namespace,
		PlaceholderResourceName:        name,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     dns.GetMultiStatefulSetName(stsName, clusterNum),
		PlaceholderExternalServiceName: dns.GetMultiExternalServiceName(stsName, clusterNum, podIdx),
		PlaceholderMongodProcessDomain: serviceDomain,
		PlaceholderMongodProcessFQDN:   dns.GetMultiClusterPodServiceFQDN(stsName, namespace, clusterNum, externalDomain, podIdx, clusterDomain),
		PlaceholderClusterName:         clusterName,
		PlaceholderClusterIndex:        fmt.Sprintf("%d", clusterNum),
	}

	if strings.HasSuffix(stsName, "mongos") {
		placeholderValues[PlaceholderMongosProcessDomain] = serviceDomain
		placeholderValues[PlaceholderMongosProcessFQDN] = dns.GetMultiClusterPodServiceFQDN(stsName, namespace, clusterNum, externalDomain, podIdx, clusterDomain)
		if externalDomain != nil {
			placeholderValues[PlaceholderMongosProcessDomain] = *externalDomain
		}
	} else {
		placeholderValues[PlaceholderMongodProcessDomain] = serviceDomain
		placeholderValues[PlaceholderMongodProcessFQDN] = dns.GetMultiClusterPodServiceFQDN(stsName, namespace, clusterNum, externalDomain, podIdx, clusterDomain)
		if externalDomain != nil {
			placeholderValues[PlaceholderMongodProcessDomain] = *externalDomain
		}
	}

	return placeholders.New(placeholderValues)
}

// AppDBInKubernetes creates or updates the StatefulSet and Service required for the AppDB.
func AppDBInKubernetes(ctx context.Context, client kubernetesClient.Client, secretClient secrets.SecretClient, opsManager *omv1.MongoDBOpsManager, podVars *env.PodEnvVars, sts appsv1.StatefulSet, serviceSelectorLabel string, log *zap.SugaredLogger) (*appsv1.StatefulSet, error) {
	sts = *sts.DeepCopy()
	cm, err := construct.AppDBAgentSettingsConfigMap(sts, opsManager.Spec.AppDB, podVars)
	if err != nil {
		return nil, fmt.Errorf("cannot render AppDB agent settings: %w", err)
	}
	var basePath string
	if secretClient.VaultClient != nil {
		basePath = secretClient.VaultClient.AppDBSecretPath()
	}
	parameters := construct.AgentSettingsOverrides(opsManager.Spec.AppDB.AutomationAgent.StartupParameters)
	if construct.ShouldEnableMonitoring(podVars) {
		// The legacy command appended the monitoring project and key after customer startup
		// options, so the monitoring key wins over a raw key. A customer mmsApiKeyExec remains
		// the credential source, matching the agent's exec-over-key preference.
		delete(parameters, "mmsApiKey")
	}
	if err := reconcileAgentSettings(ctx, client, secretClient, &sts, cm, parameters, basePath, construct.ShouldEnableMonitoring(podVars)); err != nil {
		return nil, err
	}
	if err := preserveExistingVolumeClaimTemplateMetadata(ctx, client, &sts, log); err != nil {
		return nil, err
	}

	set, err := enterprisests.CreateOrUpdateStatefulset(ctx, client, opsManager.Namespace, log, &sts)
	if err != nil {
		return nil, err
	}

	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := BuildService(namespacedName, opsManager, new(serviceSelectorLabel), nil, opsManager.Spec.AppDB.AdditionalMongodConfig.GetPortOrDefault(), omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	internalService.OwnerReferences = opsManager.AppDBOwnerReferenceForMemberCluster()

	// Adds Prometheus Port if Prometheus has been enabled.
	prom := opsManager.Spec.AppDB.Prometheus
	if prom != nil {
		//nolint:gosec // suppressing integer overflow warning for int32(prom.GetPort())
		internalService.Spec.Ports = append(internalService.Spec.Ports, corev1.ServicePort{Port: int32(prom.GetPort()), Name: "prometheus"})
	}

	return set, service.CreateOrUpdateService(ctx, client, internalService)
}

type StatefulSetIsRecreating struct {
	msg string
}

func (s StatefulSetIsRecreating) Error() string {
	return s.msg
}

// BackupDaemonInKubernetes creates or updates the StatefulSet and Services required.
func BackupDaemonInKubernetes(ctx context.Context, client kubernetesClient.Client, opsManager *omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) (*appsv1.StatefulSet, error) {
	if err := preserveExistingVolumeClaimTemplateMetadata(ctx, client, &sts, log); err != nil {
		return nil, err
	}

	set, err := enterprisests.CreateOrUpdateStatefulset(ctx, client, opsManager.Namespace, log, &sts)
	if err != nil {
		// Check if it is a k8s error or a custom one
		if _, ok := errors.AsType[enterprisests.StatefulSetCantBeUpdatedError](err); !ok {
			return nil, xerrors.Errorf("failed to create or update backup daemon statefulset: %w", err)
		}

		// In this case, we delete the old Statefulset
		stsNamespacedName := kube.ObjectKey(sts.Namespace, sts.Name)
		if deleteErr := client.DeleteStatefulSet(ctx, stsNamespacedName); deleteErr != nil {
			return nil, xerrors.Errorf("failed while trying to delete previous backup daemon statefulset: %w", deleteErr)
		}

		return nil, StatefulSetIsRecreating{"deleted the old backup stateful set and creating a new one in next reconciliation"}
	}
	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := BuildService(namespacedName, opsManager, &set.Spec.ServiceName, nil, construct.BackupDaemonServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	internalService.OwnerReferences = opsManager.OwnerReferenceForMemberCluster()
	internalService.Spec.PublishNotReadyAddresses = false

	return set, service.CreateOrUpdateService(ctx, client, internalService)
}

// OpsManagerInKubernetes creates all the required Kubernetes resources for Ops Manager.
// It creates the StatefulSet and all required services.
func OpsManagerInKubernetes(ctx context.Context, memberCluster multicluster.MemberCluster, opsManager *omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) (*appsv1.StatefulSet, error) {
	set, err := enterprisests.CreateOrUpdateStatefulset(ctx, memberCluster.Client, opsManager.Namespace, log, &sts)
	if err != nil {
		return nil, err
	}

	_, port := opsManager.GetSchemePort()

	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := BuildService(namespacedName, opsManager, &set.Spec.ServiceName, nil, port, getInternalServiceDefinition(opsManager))
	internalService.OwnerReferences = opsManager.OwnerReferenceForMemberCluster()
	internalService.Spec.PublishNotReadyAddresses = false

	// add queryable backup port to service
	if opsManager.Spec.Backup.Enabled {
		if err := addQueryableBackupPortToService(opsManager, &internalService, internalConnectivityPortName); err != nil {
			return nil, err
		}
	}

	if err := service.CreateOrUpdateService(ctx, memberCluster.Client, internalService); err != nil {
		return nil, err
	}

	namespacedName = kube.ObjectKey(opsManager.Namespace, opsManager.ExternalSvcName())
	if externalConnectivity := opsManager.GetExternalConnectivityConfigurationForMemberCluster(memberCluster.Name); externalConnectivity != nil {
		svc := BuildService(namespacedName, opsManager, &set.Spec.ServiceName, nil, port, *externalConnectivity)
		svc.OwnerReferences = opsManager.OwnerReferenceForMemberCluster()
		svc.Spec.PublishNotReadyAddresses = false

		// Need to create queryable backup service
		if opsManager.Spec.Backup.Enabled {
			if err := addQueryableBackupPortToService(opsManager, &svc, externalConnectivityPortName); err != nil {
				return nil, err
			}
		}

		if err := service.CreateOrUpdateService(ctx, memberCluster.Client, svc); err != nil {
			return nil, err
		}
	} else {
		if err := service.DeleteServiceIfItExists(ctx, memberCluster.Client, namespacedName); err != nil {
			return nil, err
		}
	}

	return set, nil
}

func getInternalServiceDefinition(opsManager *omv1.MongoDBOpsManager) omv1.MongoDBOpsManagerServiceDefinition {
	if opsManager.Spec.InternalConnectivity != nil {
		return *opsManager.Spec.InternalConnectivity
	}
	serviceDefinition := omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP}
	// For multicluster OpsManager we create ClusterIP services by default, based on the assumption
	// that the multi-cluster architecture is a multi-network configuration in most cases,
	// which makes headless services not resolve across different clusters.
	// https://github.com/istio/istio/issues/36733
	// The Spec.InternalConnectivity field allows for explicitly configuring headless services
	// and adding additional annotations to the service used for internal connectivity.
	if opsManager.Spec.IsMultiCluster() {
		serviceDefinition.ClusterIP = new("")
	}
	return serviceDefinition
}

// addQueryableBackupPortToService adds the backup port to the existing external Ops Manager service.
// this function assumes externalService is not nil.
func addQueryableBackupPortToService(opsManager *omv1.MongoDBOpsManager, service *corev1.Service, portName string) error {
	backupSvcPort, err := opsManager.Spec.BackupDaemonSvcPort()
	if err != nil {
		return xerrors.Errorf("can't parse queryable backup port: %w", err)
	}
	service.Spec.Ports[0].Name = portName
	service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{
		Name: backupPortName,
		Port: backupSvcPort,
	})
	return nil
}

// BuildService creates the Kube Service. If it should be seen externally it makes it of type NodePort that will assign
// some random port in the range 30000-32767
// Note that itself service has no dedicated IP by default ("clusterIP: None") as all mongo entities should be directly
// addressable.
// This function will update a Service object if passed, or return a new one if passed nil, this is to be able to update
// Services and to not change any attribute they might already have that needs to be maintained.
//
// When appLabel is specified, then the selector is targeting all pods (round-robin service). Usable for e.g. OpsManager service.
// When podLabel is specified, then the selector is targeting only a single pod. Used for external services or multi-cluster services.
func BuildService(namespacedName types.NamespacedName, owner v1.ObjectOwner, appLabel *string, podLabel *string, port int32, mongoServiceDefinition omv1.MongoDBOpsManagerServiceDefinition) corev1.Service {
	svcLabels := owner.GetOwnerLabels()

	selectorLabels := map[string]string{
		util.OperatorLabelName: util.OperatorLabelValue,
	}

	if appLabel != nil {
		svcLabels[appLabelKey] = *appLabel
		selectorLabels[appLabelKey] = *appLabel
	}

	if podLabel != nil {
		svcLabels[appsv1.StatefulSetPodNameLabel] = *podLabel
		selectorLabels[appsv1.StatefulSetPodNameLabel] = *podLabel
	}

	svcBuilder := service.Builder().
		SetNamespace(namespacedName.Namespace).
		SetName(namespacedName.Name).
		SetLabels(svcLabels).
		SetSelector(selectorLabels).
		SetServiceType(mongoServiceDefinition.Type).
		SetPublishNotReadyAddresses(true)

	serviceType := mongoServiceDefinition.Type
	switch serviceType {
	case corev1.ServiceTypeNodePort:
		// Service will have a NodePort
		svcPort := corev1.ServicePort{TargetPort: intstr.FromInt32(port), Name: "mongodb"}
		svcPort.NodePort = mongoServiceDefinition.Port
		if mongoServiceDefinition.Port != 0 {
			svcPort.Port = mongoServiceDefinition.Port
		} else {
			svcPort.Port = port
		}
		svcBuilder.AddPort(&svcPort).SetClusterIP("")
	case corev1.ServiceTypeLoadBalancer:
		svcPort := corev1.ServicePort{TargetPort: intstr.FromInt32(port), Name: "mongodb"}
		if mongoServiceDefinition.Port != 0 {
			svcPort.Port = mongoServiceDefinition.Port
		} else {
			svcPort.Port = port
		}
		svcBuilder.AddPort(&svcPort).SetClusterIP("")
	case corev1.ServiceTypeClusterIP:
		if mongoServiceDefinition.ClusterIP == nil {
			svcBuilder.SetClusterIP("None")
		} else {
			svcBuilder.SetClusterIP(*mongoServiceDefinition.ClusterIP)
		}
		// Service will have a named Port
		svcBuilder.AddPort(&corev1.ServicePort{Port: port, Name: "mongodb"})
	default:
		// Service will have a regular Port (unnamed)
		svcBuilder.AddPort(&corev1.ServicePort{Port: port})
	}

	if mongoServiceDefinition.Annotations != nil {
		svcBuilder.SetAnnotations(mongoServiceDefinition.Annotations)
	}

	if mongoServiceDefinition.LoadBalancerIP != "" {
		svcBuilder.SetLoadBalancerIP(mongoServiceDefinition.LoadBalancerIP)
	}

	if mongoServiceDefinition.ExternalTrafficPolicy != "" {
		svcBuilder.SetExternalTrafficPolicy(mongoServiceDefinition.ExternalTrafficPolicy)
	}

	return svcBuilder.Build()
}

// GetNonEphemeralBackupPort returns port number that will be used when we instruct the agent to use non-ephemeral port for backup monogod.
// Non-ephemeral ports are used when we set process.hostname for anything other than headless service FQDN.
func GetNonEphemeralBackupPort(mongodPort int32) int32 {
	return mongodPort + 1
}

type pvcResize struct {
	pvcName         string
	resizeIndicator int
	from            string
	to              string
}

// resourceStorageHasChanged returns 0 if both storage sizes are equal or not exist at all,
//
//	 1: toCreateVolumeClaims < desiredVolumeClaims → decrease storage
//	-1: toCreateVolumeClaims > desiredVolumeClaims → increase storage
//	 0: toCreateVolumeClaims = desiredVolumeClaims → storage stays same
func resourceStorageHasChanged(existingVolumeClaims []corev1.PersistentVolumeClaim, desiredVolumeClaims []corev1.PersistentVolumeClaim) []pvcResize {
	existingClaimByName := map[string]*corev1.PersistentVolumeClaim{}
	var pvcResizes []pvcResize

	for _, existingClaim := range existingVolumeClaims {
		existingClaimByName[existingClaim.Name] = &existingClaim
	}

	for _, desiredClaim := range desiredVolumeClaims {
		// if the desiredClaim does not exist in the list of claims, then we don't need to consider resizing, since
		// its most likely a new one
		if existingPVCClaim, ok := existingClaimByName[desiredClaim.Name]; ok {
			desiredPVCClaimStorage := desiredClaim.Spec.Resources.Requests.Storage()
			existingPVCClaimStorage := existingPVCClaim.Spec.Resources.Requests.Storage()
			if desiredPVCClaimStorage != nil && existingPVCClaimStorage != nil {
				pvcResizes = append(pvcResizes, pvcResize{
					pvcName:         desiredClaim.Name,
					resizeIndicator: existingPVCClaimStorage.Cmp(*desiredPVCClaimStorage),
					from:            existingPVCClaimStorage.String(),
					to:              desiredPVCClaimStorage.String(),
				})
			}
		}
	}

	return pvcResizes
}

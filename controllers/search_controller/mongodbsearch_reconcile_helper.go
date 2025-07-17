package search_controller

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"

	"github.com/blang/semver"
	"github.com/ghodss/yaml"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
)

const (
	MongoDBSearchIndexFieldName    = "mdbsearch-for-mongodbresourceref-index"
	MinimumSupportedMongoDBVersion = "8.2.0"
)

type OperatorSearchConfig struct {
	SearchRepo    string
	SearchName    string
	SearchVersion string
}

type MongoDBSearchReconcileHelper struct {
	client               kubernetesClient.Client
	mdbSearch            *searchv1.MongoDBSearch
	db                   SearchSourceDBResource
	operatorSearchConfig OperatorSearchConfig
}

func NewMongoDBSearchReconcileHelper(
	client kubernetesClient.Client,
	mdbSearch *searchv1.MongoDBSearch,
	db SearchSourceDBResource,
	operatorSearchConfig OperatorSearchConfig,
) *MongoDBSearchReconcileHelper {
	return &MongoDBSearchReconcileHelper{
		client:               client,
		operatorSearchConfig: operatorSearchConfig,
		mdbSearch:            mdbSearch,
		db:                   db,
	}
}

func (r *MongoDBSearchReconcileHelper) Reconcile(ctx context.Context, log *zap.SugaredLogger) workflow.Status {
	workflowStatus := r.reconcile(ctx, log)
	if _, err := commoncontroller.UpdateStatus(ctx, r.client, r.mdbSearch, workflowStatus, log); err != nil {
		return workflow.Failed(err)
	}
	return workflow.OK()
}

func (r *MongoDBSearchReconcileHelper) reconcile(ctx context.Context, log *zap.SugaredLogger) workflow.Status {
	log = log.With("MongoDBSearch", r.mdbSearch.NamespacedName())
	log.Infof("Reconciling MongoDBSearch")

	if err := ValidateSearchSource(r.db); err != nil {
		return workflow.Failed(err)
	}

	if err := r.ValidateSingleMongoDBSearchForSearchSource(ctx); err != nil {
		return workflow.Failed(err)
	}

	if err := r.ensureSearchService(ctx, r.mdbSearch); err != nil {
		return workflow.Failed(err)
	}

	mongotConfig := createMongotConfig(r.mdbSearch, r.db)
	configHash, err := r.ensureMongotConfig(ctx, mongotConfig)
	if err != nil {
		return workflow.Failed(err)
	}

	if err := r.createOrUpdateStatefulSet(ctx, log, configHash); err != nil {
		return workflow.Failed(err)
	}

	if statefulSetStatus := statefulset.GetStatefulSetStatus(ctx, r.db.NamespacedName().Namespace, r.mdbSearch.StatefulSetNamespacedName().Name, r.client); !statefulSetStatus.IsOK() {
		return statefulSetStatus
	}

	return workflow.OK()
}

func (r *MongoDBSearchReconcileHelper) createOrUpdateStatefulSet(ctx context.Context, log *zap.SugaredLogger, mongotConfigHash string) error {
	imageVersion := r.mdbSearch.Spec.Version
	if imageVersion == "" {
		imageVersion = r.operatorSearchConfig.SearchVersion
	}
	searchImage := fmt.Sprintf("%s/%s:%s", r.operatorSearchConfig.SearchRepo, r.operatorSearchConfig.SearchName, imageVersion)

	stsName := r.mdbSearch.StatefulSetNamespacedName()
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: stsName.Name, Namespace: stsName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, sts, func() error {
		stsModification := CreateSearchStatefulSetFunc(r.mdbSearch, r.db, searchImage, mongotConfigHash)
		stsModification(sts)
		return nil
	})
	if err != nil {
		return xerrors.Errorf("error creating/updating search statefulset %v: %w", stsName, err)
	}

	log.Debugf("Search statefulset %s CreateOrUpdate result: %s", stsName, op)

	return nil
}

func (r *MongoDBSearchReconcileHelper) ensureSearchService(ctx context.Context, search *searchv1.MongoDBSearch) error {
	svcName := search.SearchServiceNamespacedName()
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: svcName.Name, Namespace: svcName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, svc, func() error {
		resourceVersion := svc.ResourceVersion
		*svc = buildSearchHeadlessService(search)
		svc.ResourceVersion = resourceVersion
		return nil
	})
	if err != nil {
		return xerrors.Errorf("error creating/updating search service %v: %w", svcName, err)
	}

	zap.S().Debugf("Updated search service %v: %s", svcName, op)

	return nil
}

func (r *MongoDBSearchReconcileHelper) ensureMongotConfig(ctx context.Context, mongotConfig mongot.Config) (string, error) {
	configData, err := yaml.Marshal(mongotConfig)
	if err != nil {
		return "", err
	}

	cmName := r.mdbSearch.MongotConfigConfigMapNamespacedName()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName.Name, Namespace: cmName.Namespace}, Data: map[string]string{}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, cm, func() error {
		resourceVersion := cm.ResourceVersion

		cm.Data["config.yml"] = string(configData)

		cm.ResourceVersion = resourceVersion

		return controllerutil.SetOwnerReference(r.mdbSearch, cm, r.client.Scheme())
	})
	if err != nil {
		return "", err
	}

	zap.S().Debugf("Updated mongot config yaml config map: %v (%s) with the following configuration: %s", cmName, op, string(configData))

	return hashMongotConfig(configData), nil
}

func hashMongotConfig(mongotConfigYaml []byte) string {
	hashBytes := sha256.Sum256(mongotConfigYaml)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes[:])
}

func buildSearchHeadlessService(search *searchv1.MongoDBSearch) corev1.Service {
	labels := map[string]string{}
	name := search.SearchServiceNamespacedName().Name

	labels["app"] = name

	serviceBuilder := service.Builder().
		SetName(name).
		SetNamespace(search.Namespace).
		SetSelector(labels).
		SetLabels(labels).
		SetServiceType(corev1.ServiceTypeClusterIP).
		SetClusterIP("None").
		SetPublishNotReadyAddresses(true).
		SetOwnerReferences(search.GetOwnerReferences())

	serviceBuilder.AddPort(&corev1.ServicePort{
		Name:       "mongot",
		Protocol:   corev1.ProtocolTCP,
		Port:       search.GetMongotPort(),
		TargetPort: intstr.FromInt32(search.GetMongotPort()),
	})

	serviceBuilder.AddPort(&corev1.ServicePort{
		Name:       "metrics",
		Protocol:   corev1.ProtocolTCP,
		Port:       search.GetMongotMetricsPort(),
		TargetPort: intstr.FromInt32(search.GetMongotMetricsPort()),
	})

	return serviceBuilder.Build()
}

func createMongotConfig(search *searchv1.MongoDBSearch, db SearchSourceDBResource) mongot.Config {
	return mongot.Config{CommunityPrivatePreview: mongot.CommunityPrivatePreview{
		MongodHostAndPort:  fmt.Sprintf("%s.%s.svc.cluster.local:%d", db.DatabaseServiceName(), db.GetNamespace(), db.DatabasePort()),
		QueryServerAddress: fmt.Sprintf("localhost:%d", search.GetMongotPort()),
		KeyFilePath:        "/mongot/keyfile/keyfile",
		DataPath:           "/mongot/data/config.yml",
		Metrics: mongot.Metrics{
			Enabled: true,
			Address: fmt.Sprintf("localhost:%d", search.GetMongotMetricsPort()),
		},
		Logging: mongot.Logging{
			Verbosity: "DEBUG",
		},
	}}
}

func GetMongodConfigParameters(search *searchv1.MongoDBSearch) map[string]interface{} {
	return map[string]interface{}{
		"setParameter": map[string]interface{}{
			"mongotHost":                       mongotHostAndPort(search),
			"searchIndexManagementHostAndPort": mongotHostAndPort(search),
		},
	}
}

func mongotHostAndPort(search *searchv1.MongoDBSearch) string {
	svcName := search.SearchServiceNamespacedName()
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", svcName.Name, svcName.Namespace, search.GetMongotPort())
}

func ValidateSearchSource(db SearchSourceDBResource) error {
	err := ValidateMinVersion(db.GetMongoDBVersion(), MinimumSupportedMongoDBVersion)
	if err != nil {
		return err
	}
	if db.IsSecurityTLSConfigEnabled() {
		return xerrors.New("MongoDBSearch does not support TLS-enabled sources")
	}

	return nil
}

func (r *MongoDBSearchReconcileHelper) ValidateSingleMongoDBSearchForSearchSource(ctx context.Context) error {
	searchList := &searchv1.MongoDBSearchList{}
	if err := r.client.List(ctx, searchList, &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(MongoDBSearchIndexFieldName, r.db.GetNamespace()+"/"+r.db.Name()),
	}); err != nil {
		return xerrors.Errorf("Error listing MongoDBSearch resources for search source '%s': %w", r.db.Name(), err)
	}

	if len(searchList.Items) > 1 {
		resourceNames := make([]string, len(searchList.Items))
		for i, search := range searchList.Items {
			resourceNames[i] = search.Name
		}
		return xerrors.Errorf("Found multiple MongoDBSearch resources for search source '%s': %s", r.db.Name(), strings.Join(resourceNames, ", "))
	}

	return nil
}

func ValidateMinVersion(versionStr, minVersionStr string) error {
	version, err := semver.ParseTolerant(versionStr)
	if err != nil {
		return xerrors.Errorf("error parsing MongoDB version '%s': %w", versionStr, err)
	}
	minVersion, err := semver.ParseTolerant(minVersionStr)
	if err != nil {
		return xerrors.Errorf("error parsing MongodB minimum version '%s': %w", minVersionStr, err)
	}

	if version.LT(minVersion) {
		return xerrors.Errorf("MongoDBSearch requires MongoDB version %s or higher", minVersionStr)
	}

	return nil
}

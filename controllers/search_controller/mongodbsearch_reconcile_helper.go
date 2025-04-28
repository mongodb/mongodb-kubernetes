package search_controller

import (
	"context"
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
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
)

const (
	MongoDBSearchIndexFieldName = "mdbsearch-for-mongodbresourceref-index"
)

type OperatorSearchConfig struct {
	SearchRepo    string
	SearchName    string
	SearchVersion string
}

type MongoDBSearchReconcileHelper struct {
	client               kubernetesClient.Client
	mdbSearch            *searchv1.MongoDBSearch
	db                   construct.SearchSourceDBResource
	operatorSearchConfig OperatorSearchConfig
}

func NewMongoDBSearchReconcileHelper(
	client kubernetesClient.Client,
	mdbSearch *searchv1.MongoDBSearch,
	db construct.SearchSourceDBResource,
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

	mongotConfig := createMongotConfig(r.db)
	if err := r.ensureMongotConfig(ctx, mongotConfig); err != nil {
		return workflow.Failed(err)
	}

	if err := r.createOrUpdateStatefulSet(ctx, log); err != nil {
		return workflow.Failed(err)
	}

	if statefulSetStatus := statefulset.GetStatefulSetStatus(ctx, r.db.NamespacedName().Namespace, r.mdbSearch.StatefulSetNamespacedName().Name, r.client); !statefulSetStatus.IsOK() {
		return statefulSetStatus
	}

	return workflow.OK()
}

func (r *MongoDBSearchReconcileHelper) createOrUpdateStatefulSet(ctx context.Context, log *zap.SugaredLogger) error {
	stsName := r.mdbSearch.StatefulSetNamespacedName()
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: stsName.Name, Namespace: stsName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, sts, func() error {
		stsModification := construct.CreateSearchStatefulSetFunc(r.mdbSearch, r.db)
		stsModification(sts)
		return nil
	})
	if err != nil {
		return xerrors.Errorf("error creating/updating search statefulset %v: %w", stsName, err)
	}

	// TODO pass proper logger
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

func (r *MongoDBSearchReconcileHelper) ensureMongotConfig(ctx context.Context, mongotConfig mongot.Config) error {
	configData, err := yaml.Marshal(mongotConfig)
	if err != nil {
		return err
	}

	cmName := r.mdbSearch.MongotConfigConfigMapNamespacedName()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName.Name, Namespace: cmName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, cm, func() error {
		resourceVersion := cm.ResourceVersion

		if cm.Data == nil {
			cm.Data = map[string]string{}
		}

		cm.Data["config.yml"] = string(configData)

		cm.ResourceVersion = resourceVersion

		return controllerutil.SetOwnerReference(r.mdbSearch, cm, r.client.Scheme())
	})
	if err != nil {
		return err
	}

	zap.S().Debugf("Updated mongot config yaml config map: %v (%s) with the following configuration: %s", cmName, op, string(configData))

	return nil
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
		Protocol:   "TCP",
		Port:       27027,
		TargetPort: intstr.FromInt32(27027),
	})

	// TODO prometheus port

	return serviceBuilder.Build()
}

func createMongotConfig(db construct.SearchSourceDBResource) mongot.Config {
	return mongot.Config{CommunityPrivatePreview: mongot.CommunityPrivatePreview{
		MongodHostAndPort:  fmt.Sprintf("%s.%s.svc.cluster.local:27017", db.DatabaseServiceName(), db.GetNamespace()),
		QueryServerAddress: "localhost:27027",
		KeyFilePath:        "/mongot/keyfile/keyfile",
		DataPath:           "/mongot/data/config.yml",
		Metrics:            mongot.Metrics{},
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
	return fmt.Sprintf("%s.%s.svc.cluster.local:27027", svcName.Name, svcName.Namespace)
}

func ValidateSearchSource(db construct.SearchSourceDBResource) error {
	version, err := semver.ParseTolerant(db.GetMongoDBVersion())
	if err != nil {
		return xerrors.Errorf("error parsing MongoDB version '%s': %w", db.GetMongoDBVersion(), err)
	} else if version.Major < 8 {
		return xerrors.New("MongoDB version must be 8.0 or higher")
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

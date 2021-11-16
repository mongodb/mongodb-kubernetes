// try to always update patch versions with `go get -u=patch ./...`

module github.com/10gen/ops-manager-kubernetes

require (
	cloud.google.com/go v0.97.0
	github.com/aws/aws-sdk-go v1.41.5
	github.com/blang/semver v3.5.1+incompatible
	github.com/emicklei/go-restful v2.9.6+incompatible // indirect
	github.com/evanphx/json-patch v4.11.0+incompatible
	github.com/ghodss/yaml v1.0.0
	github.com/go-logr/logr v0.4.0
	github.com/google/uuid v1.3.0
	github.com/hashicorp/go-multierror v1.1.1
	github.com/hashicorp/vault/api v1.3.0
	github.com/imdario/mergo v0.3.12
	github.com/mongodb/mongodb-kubernetes-operator v0.7.2-0.20211116141715-93091662af59
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.11.0
	github.com/prometheus/common v0.32.1
	github.com/spf13/cast v1.4.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/objx v0.3.0
	github.com/stretchr/testify v1.7.0
	github.com/xdg/stringprep v1.0.3
	go.uber.org/zap v1.19.1
	k8s.io/api v0.22.3
	k8s.io/apimachinery v0.22.3
	k8s.io/client-go v0.22.3
	k8s.io/code-generator v0.22.2
	sigs.k8s.io/controller-runtime v0.10.3
)

go 1.16

// replace github.com/mongodb/mongodb-kubernetes-operator => ../../mongodb/mongodb-kubernetes-operator

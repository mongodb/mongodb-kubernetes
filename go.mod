// try to always update patch versions with `go get -u=patch ./...`

module github.com/10gen/ops-manager-kubernetes

require (
	github.com/aws/aws-sdk-go v1.25.48
	github.com/blang/semver v3.5.1+incompatible
	github.com/emicklei/go-restful v2.9.6+incompatible // indirect
	github.com/evanphx/json-patch v4.9.0+incompatible
	github.com/go-logr/logr v0.1.0
	github.com/go-openapi/spec v0.19.8 // indirect
	github.com/go-openapi/swag v0.19.9 // indirect
	github.com/google/uuid v1.1.2
	github.com/hashicorp/go-multierror v1.0.0
	github.com/imdario/mergo v0.3.9
	github.com/mailru/easyjson v0.7.1 // indirect
	github.com/mongodb/mongodb-kubernetes-operator v0.4.1-prerelease
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.7.1
	github.com/prometheus/common v0.10.0
	github.com/spf13/cast v1.3.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.6.1
	github.com/xdg/stringprep v1.0.0
	go.uber.org/zap v1.14.1
	k8s.io/api v0.18.6
	k8s.io/apimachinery v0.18.6
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/code-generator v0.18.6
	sigs.k8s.io/controller-runtime v0.6.4
)

go 1.13

replace k8s.io/client-go => k8s.io/client-go v0.18.6

// replace github.com/mongodb/mongodb-kubernetes-operator => ../../mongodb/mongodb-kubernetes-operator

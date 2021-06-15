// try to always update patch versions with `go get -u=patch ./...`

module github.com/10gen/ops-manager-kubernetes

require (
	github.com/aws/aws-sdk-go v1.38.45
	github.com/blang/semver v3.5.1+incompatible
	github.com/emicklei/go-restful v2.9.6+incompatible // indirect
	github.com/evanphx/json-patch v4.11.0+incompatible
	github.com/go-logr/logr v0.4.0
	github.com/go-openapi/spec v0.19.8 // indirect
	github.com/go-openapi/swag v0.19.9 // indirect
	github.com/google/uuid v1.1.2
	github.com/hashicorp/go-multierror v1.1.1
	github.com/imdario/mergo v0.3.12
	github.com/mailru/easyjson v0.7.1 // indirect
	github.com/mongodb/mongodb-kubernetes-operator v0.6.1
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.11.0
	github.com/prometheus/common v0.26.0
	github.com/spf13/cast v1.3.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/objx v0.3.0
	github.com/stretchr/testify v1.7.0
	github.com/xdg/stringprep v1.0.3
	go.uber.org/zap v1.17.0
	golang.org/x/mod v0.4.0 // indirect
	golang.org/x/tools v0.1.1-0.20210201201750-4d4ee958a9b7 // indirect
	k8s.io/api v0.21.1
	k8s.io/apimachinery v0.21.1
	k8s.io/client-go v0.21.1
	k8s.io/code-generator v0.21.1
	sigs.k8s.io/controller-runtime v0.9.0
)

go 1.15

//replace github.com/mongodb/mongodb-kubernetes-operator => ../../mongodb/mongodb-kubernetes-operator

// try to always update patch versions with `go get -u=patch ./...`

module github.com/10gen/ops-manager-kubernetes

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blang/semver v3.5.1+incompatible
	github.com/emicklei/go-restful v2.9.6+incompatible // indirect
	github.com/evanphx/json-patch v4.5.0+incompatible
	github.com/go-openapi/jsonreference v0.19.3 // indirect
	github.com/go-openapi/spec v0.19.7 // indirect
	github.com/go-openapi/swag v0.19.9 // indirect
	github.com/golang/protobuf v1.3.5 // indirect
	github.com/google/uuid v1.1.1
	github.com/hashicorp/go-multierror v1.0.0
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/imdario/mergo v0.3.9
	github.com/json-iterator/go v1.1.9 // indirect
	github.com/mailru/easyjson v0.7.1 // indirect
	github.com/mongodb/mongodb-kubernetes-operator v0.0.3 // indirect
	github.com/operator-framework/operator-sdk v0.17.0 // indirect
	github.com/pkg/errors v0.9.1
	github.com/prometheus/procfs v0.0.11 // indirect
	github.com/spf13/cast v1.3.1
	github.com/stretchr/testify v1.4.0
	github.com/xdg/stringprep v1.0.0
	go.uber.org/zap v1.14.1
	k8s.io/api v0.17.5
	k8s.io/apiextensions-apiserver v0.17.5 // indirect
	k8s.io/apimachinery v0.17.5
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/code-generator v0.17.5
	sigs.k8s.io/controller-runtime v0.5.2
	sigs.k8s.io/testing_frameworks v0.1.2 // indirect
)

go 1.13

replace k8s.io/client-go => k8s.io/client-go v0.17.5

//replace github.com/mongodb/mongodb-kubernetes-operator => ../../mongodb/mongodb-kubernetes-operator

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
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v0.9.4 // indirect
	github.com/prometheus/procfs v0.0.11 // indirect
	github.com/spf13/cast v1.3.1
	github.com/stretchr/testify v1.4.0
	github.com/xdg/stringprep v1.0.0
	go.uber.org/atomic v1.5.1 // indirect
	go.uber.org/zap v1.13.0
	k8s.io/api v0.16.9
	k8s.io/apimachinery v0.16.9
	k8s.io/client-go v0.16.9
	k8s.io/code-generator v0.16.9
	sigs.k8s.io/controller-runtime v0.4.0
)

go 1.13

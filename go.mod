// DO NOT MODIFY: AUTO GENERATED FILE
// modify template scripts/dev/go.mod.jinja and run scripts/dev/update_k8s_version_go_mod.py

module github.com/10gen/ops-manager-kubernetes

require (
	github.com/blang/semver v3.5.0+incompatible
	github.com/google/uuid v1.1.1
	github.com/imdario/mergo v0.3.8
	github.com/pkg/errors v0.9.1
	github.com/spf13/cast v1.3.1
	github.com/stretchr/testify v1.4.0
	github.com/xdg/stringprep v1.0.0
	go.uber.org/zap v1.13.0
	k8s.io/api v0.15.9 // kubernetes-1.15.9
	k8s.io/apimachinery v0.15.9 // kubernetes-1.15.9
	k8s.io/client-go v0.15.9 // kubernetes-1.15.9
	k8s.io/code-generator v0.15.9 // kubernetes-1.15.9
	sigs.k8s.io/controller-runtime v0.3.0
)

go 1.13

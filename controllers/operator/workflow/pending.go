package workflow

import (
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
)

// pendingStatus indicates that the reconciliation process must be suspended and CR should get "Pending" status
type pendingStatus struct {
	commonStatus
	retryInSeconds int
	requeue        bool
}

func Pending(msg string, params ...interface{}) *pendingStatus {
	return &pendingStatus{commonStatus: newCommonStatus(msg, params...), retryInSeconds: 10}
}

func (p *pendingStatus) WithWarnings(warnings []status.Warning) *pendingStatus {
	p.warnings = warnings
	return p
}

func (p *pendingStatus) WithRetry(retryInSeconds int) *pendingStatus {
	p.retryInSeconds = retryInSeconds
	return p
}

func (p *pendingStatus) WithResourcesNotReady(resourcesNotReady []status.ResourceNotReady) *pendingStatus {
	p.resourcesNotReady = resourcesNotReady
	return p
}

func (p *pendingStatus) WithAdditionalOptions(options ...status.Option) *pendingStatus {
	p.options = options
	return p
}

func (p pendingStatus) ReconcileResult() (reconcile.Result, error) {
	return reconcile.Result{RequeueAfter: time.Second * time.Duration(p.retryInSeconds), Requeue: p.requeue}, nil
}

func (p pendingStatus) IsOK() bool {
	return false
}

func (p pendingStatus) Merge(other Status) Status {
	switch v := other.(type) {
	// Pending messages are just merged together
	case pendingStatus:
		return mergedPending(p, v)
	case failedStatus:
		return v
	}
	return p
}

func (p pendingStatus) OnErrorPrepend(msg string) Status {
	p.commonStatus.prependMsg(msg)
	return p
}

func (p pendingStatus) StatusOptions() []status.Option {
	options := p.statusOptions()
	// Add any custom options here
	return options
}

func (p pendingStatus) Phase() status.Phase {
	return status.PhasePending
}

func (p pendingStatus) Log(log *zap.SugaredLogger) {
	log.Info(stringutil.UpperCaseFirstChar(p.msg))
}

func mergedPending(p1, p2 pendingStatus) pendingStatus {
	p := Pending("%s, %s", p1.msg, p2.msg)
	p.warnings = append(p1.warnings, p2.warnings...)
	p.resourcesNotReady = make([]status.ResourceNotReady, 0)
	p.resourcesNotReady = append(p.resourcesNotReady, p1.resourcesNotReady...)
	p.resourcesNotReady = append(p.resourcesNotReady, p2.resourcesNotReady...)
	return *p
}

func (p *pendingStatus) Requeue() Status {
	p.requeue = true
	p.retryInSeconds = 0
	return p
}

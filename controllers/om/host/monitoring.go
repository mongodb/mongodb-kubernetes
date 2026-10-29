package host

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// StopMonitoring stops Ops Manager monitoring of hosts, which removes them from
// the Processes view. It tries every host and returns an error that wraps every
// per-host failure.
//
// When tolerateBackupRejection is true, a CANNOT_DELETE_HOST_BACKUP_ENABLED error
// is logged and ignored. Ops Manager rejects only the last host of a backed-up
// replica set, so that host stays to anchor the backup and the other hosts are
// still removed.
func StopMonitoring(ctx context.Context, getRemover GetRemover, hostnames []string, tolerateBackupRejection bool, log *zap.SugaredLogger) error {
	if len(hostnames) == 0 {
		return nil
	}

	hosts, err := getRemover.GetHosts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, hostname := range hostnames {
		found := false
		for _, h := range hosts.Results {
			if h.Hostname == hostname {
				found = true
				err = getRemover.RemoveHost(ctx, h.Id)
				switch {
				case err == nil:
					log.Debugf("Removed the host %s from monitoring in Ops Manager", h.Hostname)
				case tolerateBackupRejection && isBackupEnabledRejection(err):
					log.Warnf("Ops Manager keeps monitoring host %s because backup is still enabled for its replica set: %s", h.Hostname, err)
				default:
					log.Warnf("Failed to remove host %s from monitoring in Ops Manager: %s", h.Hostname, err)
					errs = append(errs, fmt.Errorf("host %s: %w", h.Hostname, err))
				}
				break
			}
		}
		if !found {
			log.Warnf("Unable to remove monitoring on host %s as it was not found", hostname)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to remove some hosts from monitoring in Ops manager: %w", errors.Join(errs...))
	}
	return nil
}

func isBackupEnabledRejection(err error) bool {
	apiErr, ok := errors.AsType[*apierror.Error](err)
	return ok && apiErr.ErrorCode == apierror.CannotDeleteHostBackupEnabled
}

// stopMonitoringHosts removes monitoring for this list of hosts from Ops Manager.
func stopMonitoringHosts(ctx context.Context, getRemover GetRemover, hosts []string, log *zap.SugaredLogger) error {
	if len(hosts) == 0 {
		return nil
	}

	if err := StopMonitoring(ctx, getRemover, hosts, false, log); err != nil {
		return xerrors.Errorf("Failed to stop monitoring on hosts %s: %w", hosts, err)
	}

	return nil
}

// CalculateDiffAndStopMonitoringHosts checks hosts that are present in hostsBefore but not hostsAfter, and removes
// monitoring from them.
func CalculateDiffAndStopMonitoring(ctx context.Context, getRemover GetRemover, hostsBefore, hostsAfter []string, log *zap.SugaredLogger) error {
	return stopMonitoringHosts(ctx, getRemover, util.FindLeftDifference(hostsBefore, hostsAfter), log)
}

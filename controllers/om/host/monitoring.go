package host

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// StopMonitoring stops Ops Manager monitoring of hosts, which removes them from
// the Processes view. It tries every host and returns an error that wraps every
// per-host failure.
//
// Ops Manager refuses to remove the last host of a backed-up replica set. It
// reports that refusal as a generic 500, so the response cannot identify it.
// maxRemainingHosts is the number of matched hosts that must stay for the
// operation to be complete: one per backed-up replica set on deletion, and zero
// otherwise. When a removal fails, a matching count is the intended end state,
// not a failure.
func StopMonitoring(ctx context.Context, getRemover GetRemover, hostnames []string, maxRemainingHosts int, log *zap.SugaredLogger) error {
	if len(hostnames) == 0 {
		return nil
	}

	hosts, err := getRemover.GetHosts(ctx)
	if err != nil {
		return err
	}

	matched := make([]Host, 0, len(hostnames))
	for _, hostname := range hostnames {
		found := false
		for _, h := range hosts.Results {
			if h.Hostname == hostname {
				matched = append(matched, h)
				found = true
				break
			}
		}
		if !found {
			log.Warnf("Unable to remove monitoring on host %s as it was not found", hostname)
		}
	}

	var errs []error
	for _, h := range matched {
		if err := getRemover.RemoveHost(ctx, h.Id); err != nil {
			log.Warnf("Failed to remove host %s from monitoring in Ops Manager: %s", h.Hostname, err)
			errs = append(errs, fmt.Errorf("host %s: %w", h.Hostname, err))
			continue
		}
		log.Debugf("Removed the host %s from monitoring in Ops Manager", h.Hostname)
	}

	if len(errs) == 0 {
		return nil
	}

	remaining, verifyErr := remainingHosts(ctx, getRemover, matched)
	if verifyErr != nil {
		errs = append(errs, fmt.Errorf("failed to verify remaining hosts: %w", verifyErr))
	} else if remaining <= maxRemainingHosts {
		log.Infof("Host removal finished with %d of %d hosts still monitored", remaining, len(matched))
		return nil
	}

	return fmt.Errorf("failed to remove some hosts from monitoring in Ops manager: %w", errors.Join(errs...))
}

// remainingHosts counts how many of the matched hosts are still monitored.
func remainingHosts(ctx context.Context, getter Getter, matched []Host) (int, error) {
	hosts, err := getter.GetHosts(ctx)
	if err != nil {
		return 0, err
	}
	present := make(map[string]struct{}, len(hosts.Results))
	for _, h := range hosts.Results {
		present[h.Hostname] = struct{}{}
	}
	var remaining int
	for _, h := range matched {
		if _, ok := present[h.Hostname]; ok {
			remaining++
		}
	}
	return remaining, nil
}

// stopMonitoringHosts removes monitoring for this list of hosts from Ops Manager.
func stopMonitoringHosts(ctx context.Context, getRemover GetRemover, hosts []string, log *zap.SugaredLogger) error {
	if len(hosts) == 0 {
		return nil
	}

	if err := StopMonitoring(ctx, getRemover, hosts, 0, log); err != nil {
		return xerrors.Errorf("Failed to stop monitoring on hosts %s: %w", hosts, err)
	}

	return nil
}

// CalculateDiffAndStopMonitoringHosts checks hosts that are present in hostsBefore but not hostsAfter, and removes
// monitoring from them.
func CalculateDiffAndStopMonitoring(ctx context.Context, getRemover GetRemover, hostsBefore, hostsAfter []string, log *zap.SugaredLogger) error {
	return stopMonitoringHosts(ctx, getRemover, util.FindLeftDifference(hostsBefore, hostsAfter), log)
}

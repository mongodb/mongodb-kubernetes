package host

import (
	"errors"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// StopMonitoring will stop OM monitoring of hosts, which will then
// make OM stop displaying old hosts from Processes view.
// Note, that the method tries to delete as many hosts as possible and doesn't give up on errors, returns
// the last error instead
func StopMonitoring(getRemover GetRemover, hostnames []string, log *zap.SugaredLogger) error {
	if len(hostnames) == 0 {
		return nil
	}

	hosts, err := getRemover.GetHosts()
	if err != nil {
		return err
	}
	errorHappened := false
	for _, hostname := range hostnames {
		found := false
		for _, h := range hosts.Results {
			if h.Hostname == hostname {
				found = true
				err = getRemover.RemoveHost(h.Id)
				if err != nil {
					log.Warnf("Failed to remove host %s from monitoring in Ops Manager: %s", h.Hostname, err)
					errorHappened = true
				} else {
					log.Debugf("Removed the host %s from monitoring in Ops Manager", h.Hostname)
				}
				break
			}
		}
		if !found {
			log.Warnf("Unable to remove monitoring on host %s as it was not found", hostname)
		}
	}

	if errorHappened {
		return errors.New("failed to remove some hosts from monitoring in Ops manager")
	}
	return nil
}

// stopMonitoringHosts removes monitoring for this list of hosts from Ops Manager.
func stopMonitoringHosts(getRemover GetRemover, hosts []string, log *zap.SugaredLogger) error {
	if len(hosts) == 0 {
		return nil
	}

	if err := StopMonitoring(getRemover, hosts, log); err != nil {
		return xerrors.Errorf("Failed to stop monitoring on hosts %s: %w", hosts, err)
	}

	return nil
}

// CalculateDiffAndStopMonitoringHosts checks hosts that are present in hostsBefore but not hostsAfter, and removes
// monitoring from them.
func CalculateDiffAndStopMonitoring(getRemover GetRemover, hostsBefore, hostsAfter []string, log *zap.SugaredLogger) error {
	return stopMonitoringHosts(getRemover, util.FindLeftDifference(hostsBefore, hostsAfter), log)
}

// GetMonitoredHostnamesForRS returns hostnames from OM's monitored hosts that belong to the
// specified replica set. Hosts are identified by matching the hostname pattern:
// {rsName}-{ordinal}.{serviceFQDN}
//
// Note: The OM API supports server-side filtering via the clusterId query parameter:
// GET /groups/{PROJECT-ID}/hosts?clusterId={CLUSTER-ID}
// See: https://www.mongodb.com/docs/ops-manager/current/reference/api/hosts/get-all-hosts-in-group/
// If we have access to the cluster ID reliably, we can take advantage of it
func GetMonitoredHostnamesForRS(getter Getter, rsName, serviceFQDN string) ([]string, error) {
	allHosts, err := getter.GetHosts()
	if err != nil {
		return nil, xerrors.Errorf("failed to get hosts from OM: %w", err)
	}

	prefix := rsName + "-"
	var rsHosts []string
	for _, h := range allHosts.Results {
		if strings.HasPrefix(h.Hostname, prefix) && strings.Contains(h.Hostname, serviceFQDN) {
			rsHosts = append(rsHosts, h.Hostname)
		}
	}
	return rsHosts, nil
}

// RemoveUndesiredMonitoringHosts ensures only the desired hosts are monitored for a replica set.
// This is idempotent: it compares actual monitored hosts against desired and removes any extras.
// Should be called on every reconciliation to ensure orphaned hosts are cleaned up.
func RemoveUndesiredMonitoringHosts(getRemover GetRemover, rsName, serviceFQDN string, hostsDesired []string, log *zap.SugaredLogger) error {
	hostsMonitored, err := GetMonitoredHostnamesForRS(getRemover, rsName, serviceFQDN)
	if err != nil {
		return err
	}

	// Reuse existing diff calculation and removal logic
	return CalculateDiffAndStopMonitoring(getRemover, hostsMonitored, hostsDesired, log)
}

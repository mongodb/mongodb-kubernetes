package om

import (
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

type Host struct {
	Results []HostList `json:"results"`
}

type HostList struct {
	Id       string `json:"id"`
	Hostname string `json:"hostname"`
}

// StopMonitoring will stop OM monitoring of hosts, which will then
// make OM stop displaying old hosts from Processes view.
// Note, that the method tries to delete as many hosts as possible and doesn't give up on errors, returns
// the last error instead
func StopMonitoring(omClient Connection, hostnames []string, log *zap.SugaredLogger) error {
	if len(hostnames) == 0 {
		return nil
	}

	hosts, err := omClient.GetHosts()
	if err != nil {
		return err
	}
	errorHappened := false
	for _, hostname := range hostnames {
		found := false
		for _, host := range hosts.Results {
			if host.Hostname == hostname {
				found = true
				err = omClient.RemoveHost(host.Id)
				if err != nil {
					log.Warnf("Failed to remove host %s from monitoring in Ops Manager: %s", host.Hostname, err)
					errorHappened = true
				} else {
					log.Debugf("Removed the host %s from monitoring in Ops Manager", host.Hostname)
				}
				break
			}
		}
		if !found {
			log.Warnf("Unable to remove monitoring on host %s as it was not found", hostname)
		}
	}

	if errorHappened {
		return errors.New("Failed to remove some hosts from monitoring in Ops manager")
	}
	return nil
}

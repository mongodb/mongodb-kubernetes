package om

import (
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
// will make OM stop displaying old hosts from Processes view.
func StopMonitoring(omClient OmConnection, hostnames []string, log *zap.SugaredLogger) error {
	if len(hostnames) == 0 {
		return nil
	}

	hosts, err := omClient.GetHosts()
	if err != nil {
		return err
	}

	for _, hostname := range hostnames {
		found := false
		for _, host := range hosts.Results {
			if host.Hostname == hostname {
				found = true
				err := omClient.RemoveHost(host.Id)
				if err != nil {
					log.Errorf("Failed to remove host %s from monitoring in Ops Manager: %s", host.Hostname, err)
				} else {
					log.Debugf("Removed the host %s from monitoring in Ops Manager", host.Hostname)
				}
				break
			}
		}
		if !found {
			log.Errorf("Unable to remove monitoring on host %s as it was not found", hostname)
		}
	}

	return nil
}

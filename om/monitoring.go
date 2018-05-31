package om

import (
	"errors"
	"fmt"
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
func StopMonitoring(omClient OmConnection, hostnames []string) error {
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
				omClient.RemoveHost(host.Id)
				break
			}
		}
		if !found {
			return errors.New(fmt.Sprintf("Unable to remove monitoring on host %s", hostname))
		}
	}

	return nil
}

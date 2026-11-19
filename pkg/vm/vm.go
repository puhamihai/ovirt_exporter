// SPDX-License-Identifier: MIT

package vm

// VMs is a collection of virtual machines
type VMs struct {
	VMs []VM `xml:"vm"`
}

// VM represents the virutal machine resource
type VM struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name"`
	Host struct {
		ID string `xml:"id,attr"`
	} `xml:"host,omitempty"`
	Cluster struct {
		ID string `xml:"id,attr"`
	} `xml:"cluster,omitempty"`
	Status string `xml:"status"`
	CPU    struct {
		Topology struct {
			Cores   int `xml:"cores"`
			Sockets int `xml:"sockets"`
			Threads int `xml:"threads"`
		} `xml:"topology"`
	} `xml:"cpu"`
	HasIllegalImages       bool `xml:"has_illegal_images"`
	HighAvailability struct {
		Enabled bool `xml:"enabled"`
	} `xml:"high_availability"`
	GuestOperatingSystem struct {
		Family  string `xml:"family"`
		Version struct {
			Full string `xml:"full_version"`
		} `xml:"version"`
	} `xml:"guest_operating_system"`
	NextRunConfigurationExists bool   `xml:"next_run_configuration_exists"`
	Fqdn                       string `xml:"fqdn"`
}

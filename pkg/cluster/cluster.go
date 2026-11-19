// SPDX-License-Identifier: MIT

package cluster

// Clusters is a collection of clusters
type Clusters struct {
	Clusters []Cluster `xml:"cluster"`
}

// Cluster represents the cluster resource
type Cluster struct {
	ID          string `xml:"id,attr"`
	Name        string `xml:"name"`
	Description string `xml:"description"`
	DataCenter  struct {
		ID string `xml:"id,attr"`
	} `xml:"data_center"`
	Version struct {
		Major int `xml:"major"`
		Minor int `xml:"minor"`
	} `xml:"version"`
	BallooningEnabled bool `xml:"ballooning_enabled"`
	KSM               struct {
		Enabled bool `xml:"enabled"`
	} `xml:"ksm"`
	MemoryPolicy struct {
		OverCommit struct {
			Percent int `xml:"percent"`
		} `xml:"over_commit"`
	} `xml:"memory_policy"`
	FencingPolicy struct {
		Enabled bool `xml:"enabled"`
	} `xml:"fencing_policy"`
	HAReservation     bool `xml:"ha_reservation"`
	UpgradeInProgress bool `xml:"upgrade_in_progress"`
}

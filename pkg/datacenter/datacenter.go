// SPDX-License-Identifier: MIT

package datacenter

// DataCenters is a collection of data centers
type DataCenters struct {
	DataCenters []DataCenter `xml:"data_center"`
}

// DataCenter represents the data center resource
type DataCenter struct {
	ID        string `xml:"id,attr"`
	Name      string `xml:"name"`
	Status    string `xml:"status"`
	QuotaMode string `xml:"quota_mode"`
	Local     bool   `xml:"local"`
}

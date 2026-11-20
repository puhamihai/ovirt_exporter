// SPDX-License-Identifier: MIT

package storagedomain

// StorageDomains is a collection of storage domains
type StorageDomains struct {
	Domains []StorageDomain `xml:"storage_domain"`
}

// StorageDomain represents the storage domain resource.
//
// The same type decodes both listings the engine offers:
//
//   - GET /storagedomains (system scope): Status is set only for a domain that is
//     attached to no data center ("unattached"); for an attached domain the
//     element is absent, so nothing here says whether it is serving I/O.
//   - GET /datacenters/{id}/storagedomains (data-center scope): Status is the
//     per-data-center state (active, inactive, maintenance, locked, ...).
//
// ExternalStatus is the health reported by an external provider plug-in and
// stays "ok" on an engine without one, whatever the domain is doing.
type StorageDomain struct {
	ID      string `xml:"id,attr"`
	Name    string `xml:"name,omitempty"`
	Storage struct {
		Path string `xml:"path,omitempty"`
		Type string `xml:"type,omitempty"`
	} `xml:"storage,omitempty"`
	Type           string  `xml:"type,omitempty"`
	Available      float64 ` xml:"available,omitempty"`
	Committed      float64 `xml:"committed,omitempty"`
	Used           float64 `xml:"used,omitempty"`
	Status         string  `xml:"status,omitempty"`
	ExternalStatus string  `xml:"external_status,omitempty"`
	Master         bool    `xml:"master,omitempty"`
	DataCenters    struct {
		DataCenter []struct {
			ID string `xml:"id,attr"`
		} `xml:"data_center"`
	} `xml:"data_centers,omitempty"`
}

// DataCenterIDs returns the ids of the data centers the domain is attached to
// (ISO and export domains can be attached to several).
func (d *StorageDomain) DataCenterIDs() []string {
	ids := make([]string, 0, len(d.DataCenters.DataCenter))
	for _, dc := range d.DataCenters.DataCenter {
		if dc.ID != "" {
			ids = append(ids, dc.ID)
		}
	}
	return ids
}

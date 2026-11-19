// SPDX-License-Identifier: MIT

package disk

import "github.com/czerwonk/ovirt_exporter/pkg/storagedomain"

// Disks is the /disks collection
type Disks struct {
	Disks []Disk `xml:"disk"`
}

// Disk represents the disk resource
type Disk struct {
	ID              string                        `xml:"id,attr"`
	Name            string                        `xml:"name,omitempty"`
	Alias           string                        `xml:"alias,omitempty"`
	ProvisionedSize uint64                        `xml:"provisioned_size,omitempty"`
	ActualSize      uint64                        `xml:"actual_size,omitempty"`
	TotalSize       uint64                        `xml:"total_size,omitempty"`
	Format          string                        `xml:"format,omitempty"`
	Sparse          bool                          `xml:"sparse,omitempty"`
	StorageType     string                        `xml:"storage_type,omitempty"`
	Status          string                        `xml:"status,omitempty"`
	StorageDomains  *storagedomain.StorageDomains `xml:"storage_domains,omitempty"`
}

// StorageDomainID returns the id of the (first) storage domain holding the disk
func (d *Disk) StorageDomainID() string {
	if d.StorageDomains == nil || len(d.StorageDomains.Domains) == 0 {
		return ""
	}

	return d.StorageDomains.Domains[0].ID
}

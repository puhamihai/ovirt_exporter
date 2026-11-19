// SPDX-License-Identifier: MIT

package disk

import (
	"context"

	"github.com/czerwonk/ovirt_exporter/pkg/collector"
)

// List retrieves every disk the engine knows about in ONE request and indexes
// it by id. The /disks collection carries provisioned/actual/total size, format
// and sparseness for all disks, so callers never need a per-disk GET.
func List(ctx context.Context, cl collector.Client) (map[string]*Disk, error) {
	ds := Disks{}
	if err := cl.GetAndParse(ctx, "disks", &ds); err != nil {
		return nil, err
	}

	m := make(map[string]*Disk, len(ds.Disks))
	for i := range ds.Disks {
		m[ds.Disks[i].ID] = &ds.Disks[i]
	}

	return m, nil
}

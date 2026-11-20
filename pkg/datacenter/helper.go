// SPDX-License-Identifier: MIT

package datacenter

import (
	"context"
	"fmt"

	"github.com/czerwonk/ovirt_exporter/pkg/collector"
)

// Get retrieves data center information
func Get(ctx context.Context, id string, cl collector.Client) (*DataCenter, error) {
	path := fmt.Sprintf("datacenters/%s", id)

	dc := DataCenter{}
	err := cl.GetAndParse(ctx, path, &dc)
	if err != nil {
		return nil, err
	}

	return &dc, nil
}

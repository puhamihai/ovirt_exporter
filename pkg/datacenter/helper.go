// SPDX-License-Identifier: MIT

package datacenter

import (
	"context"
	"fmt"
	"sync"

	"github.com/czerwonk/ovirt_exporter/pkg/collector"
	log "github.com/sirupsen/logrus"
)

var (
	cacheMutex  = sync.Mutex{}
	statusCache = make(map[string]string)
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

// Name retrieves data center name by ID
func Name(ctx context.Context, id string, cl collector.Client) string {
	dc, err := Get(ctx, id, cl)
	if err != nil {
		log.Error(err)
		return ""
	}

	return dc.Name
}

// Status retrieves data center status by ID (cached)
func Status(ctx context.Context, id string, cl collector.Client) string {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	if s, found := statusCache[id]; found {
		return s
	}

	dc, err := Get(ctx, id, cl)
	if err != nil {
		log.Error(err)
		return ""
	}

	statusCache[id] = dc.Status
	return dc.Status
}

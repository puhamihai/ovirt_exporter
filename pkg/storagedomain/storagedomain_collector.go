// SPDX-License-Identifier: MIT

package storagedomain

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"github.com/czerwonk/ovirt_exporter/pkg/collector"
	"github.com/czerwonk/ovirt_exporter/pkg/datacenter"
	"github.com/czerwonk/ovirt_exporter/pkg/metric"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
)

const prefix = "ovirt_storage_"

var (
	availableDesc    *prometheus.Desc
	usedDesc         *prometheus.Desc
	committedDesc    *prometheus.Desc
	masterDesc       *prometheus.Desc
	upDesc           *prometheus.Desc
	statusDesc       *prometheus.Desc
	labelNames       []string
	statusLabelNames []string
	maintenanceRegex *regexp.Regexp
)

func init() {
	labelNames = []string{"name", "type", "path"}
	availableDesc = prometheus.NewDesc(prefix+"available_bytes", "Available space in bytes", labelNames, nil)
	usedDesc = prometheus.NewDesc(prefix+"used_bytes", "Used space in bytes", labelNames, nil)
	committedDesc = prometheus.NewDesc(prefix+"committed_bytes", "Committed space in bytes", labelNames, nil)
	upDesc = prometheus.NewDesc(prefix+"up", "Storage domain is active in its data center (1), in maintenance (2) or anything else (0); an unattached domain reports its external status", labelNames, nil)
	masterDesc = prometheus.NewDesc(prefix+"master", "Storage domain is master", labelNames, nil)
	// ovirt_storage_up collapses the per-data-center state into 1/2/0; the raw
	// state travels in the "status" label of this info metric, one series per
	// data center the domain is attached to (datacenter="" when unattached).
	statusLabelNames = append(append([]string{}, labelNames...), "datacenter", "status")
	statusDesc = prometheus.NewDesc(prefix+"status", "Raw storage domain status in a data center as reported by the engine; always 1, with the state in the \"status\" label", statusLabelNames, nil)
	maintenanceRegex = regexp.MustCompile(`maintenance`)
}

// StorageDomainCollector collects storage domain statistics from oVirt
type StorageDomainCollector struct {
	cc              *collector.CollectorContext
	collectDuration prometheus.Observer
	rootCtx         context.Context
}

// dcState is the status of one storage domain inside one data center.
type dcState struct {
	dcName string
	status string
}

// NewCollector creates a new collector
func NewCollector(ctx context.Context, cc *collector.CollectorContext, collectDuration prometheus.Observer) prometheus.Collector {
	return &StorageDomainCollector{
		rootCtx:         ctx,
		cc:              cc,
		collectDuration: collectDuration,
	}
}

// Collect implements Prometheus Collector interface
func (c *StorageDomainCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, span := c.cc.Tracer().Start(c.rootCtx, "StorageDomainCollector.Collect")
	defer span.End()

	c.cc.SetMetricsCh(ch)

	timer := prometheus.NewTimer(c.collectDuration)
	defer timer.ObserveDuration()

	// The system-scope listing and the per-data-center listings are independent,
	// so they are requested at the same time (the client caps requests in flight).
	var (
		wg        sync.WaitGroup
		states    map[string][]dcState
		failedDCs map[string]bool
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		states, failedDCs = c.collectDataCenterStates(ctx, span)
	}()

	s := StorageDomains{}
	err := c.cc.Client().GetAndParse(ctx, "storagedomains", &s)
	if err != nil {
		c.cc.HandleError(err, span)
		wg.Wait()
		return
	}
	wg.Wait()

	for _, d := range s.Domains {
		c.collectMetricsForDomain(d, states[d.ID], failedDCs)
	}
}

// Describe implements Prometheus Collector interface
func (c *StorageDomainCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- upDesc
	ch <- statusDesc
	ch <- masterDesc
	ch <- availableDesc
	ch <- usedDesc
	ch <- committedDesc
}

// collectDataCenterStates fetches GET /datacenters/{id}/storagedomains for every
// data center (one request per data center, in parallel) and returns the state
// of each storage domain per data center, keyed by storage domain id, plus the
// ids of the data centers whose listing could not be fetched.
//
// This is the only place the engine reports whether an ATTACHED domain is
// serving I/O. The system-scope GET /storagedomains has no status element for
// an attached domain, and its external_status is the health reported by an
// external provider plug-in, which stays "ok" whatever happens. A domain the
// engine deactivates because no host can see its LUNs ("Storage Domain ... was
// deactivated by system because it's not visible by any of the hosts") still
// has external_status "ok".
func (c *StorageDomainCollector) collectDataCenterStates(ctx context.Context, span trace.Span) (map[string][]dcState, map[string]bool) {
	states := make(map[string][]dcState)
	failed := make(map[string]bool)

	dcs := datacenter.DataCenters{}
	if err := c.cc.Client().GetAndParse(ctx, "datacenters", &dcs); err != nil {
		// Without the data center list no attached domain can be judged; mark
		// every data center as failed by using the wildcard key.
		c.cc.HandleError(fmt.Errorf("storage domain status: list data centers: %w", err), span)
		failed["*"] = true
		return states, failed
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, dc := range dcs.DataCenters {
		wg.Add(1)
		go func(dc datacenter.DataCenter) {
			defer wg.Done()

			sds := StorageDomains{}
			path := fmt.Sprintf("datacenters/%s/storagedomains", dc.ID)
			if err := c.cc.Client().GetAndParse(ctx, path, &sds); err != nil {
				c.cc.HandleError(fmt.Errorf("storage domain status: data center %s: %w", dc.Name, err), span)
				mu.Lock()
				failed[dc.ID] = true
				mu.Unlock()
				return
			}

			mu.Lock()
			for _, sd := range sds.Domains {
				states[sd.ID] = append(states[sd.ID], dcState{dcName: dc.Name, status: sd.Status})
			}
			mu.Unlock()
		}(dc)
	}
	wg.Wait()

	return states, failed
}

func (c *StorageDomainCollector) collectMetricsForDomain(domain StorageDomain, states []dcState, failedDCs map[string]bool) {
	d := &domain
	l := []string{d.Name, d.Type, d.Storage.Path}

	c.cc.RecordMetrics(
		metric.MustCreate(masterDesc, boolToFloat(d.Master), l),
		metric.MustCreate(availableDesc, float64(d.Available), l),
		metric.MustCreate(usedDesc, float64(d.Used), l),
		metric.MustCreate(committedDesc, float64(d.Committed), l),
	)

	if c.statusUnknown(d, failedDCs) {
		// The domain is attached to a data center whose listing failed this
		// scrape, so its worst-of-data-centers state cannot be computed. Judging
		// it from external_status or from the other data centers alone would
		// report a domain in an unknown state as up, so ovirt_storage_up is left
		// absent; the states that are known are still exposed.
		if len(states) > 0 {
			for _, m := range c.statusMetrics(d, states, l) {
				c.cc.RecordMetrics(m)
			}
		}
		return
	}

	c.cc.RecordMetrics(c.upMetric(d, states, l))
	for _, m := range c.statusMetrics(d, states, l) {
		c.cc.RecordMetrics(m)
	}
}

// statusUnknown reports whether the domain is attached to a data center whose
// storage domain listing could not be fetched.
func (c *StorageDomainCollector) statusUnknown(d *StorageDomain, failedDCs map[string]bool) bool {
	if failedDCs["*"] && len(d.DataCenterIDs()) > 0 {
		return true
	}
	for _, id := range d.DataCenterIDs() {
		if failedDCs[id] {
			return true
		}
	}
	return false
}

// upMetric encodes the storage domain state the way ovirt_host_up and
// ovirt_datacenter_up do: active 1, (preparing for) maintenance 2, anything
// else 0. A domain attached to several data centers reports the worst of them
// (0 beats 2 beats 1). An unattached domain has no data-center state; it keeps
// the external_status encoding ("ok" is 1) so that a deliberately
// detached domain does not become a permanent alert.
func (c *StorageDomainCollector) upMetric(d *StorageDomain, states []dcState, labelValues []string) prometheus.Metric {
	if len(states) == 0 {
		return metric.MustCreate(upDesc, boolToFloat(d.ExternalStatus == "ok"), labelValues)
	}

	up := statusToFloat(states[0].status)
	for _, s := range states[1:] {
		up = worst(up, statusToFloat(s.status))
	}

	return metric.MustCreate(upDesc, up, labelValues)
}

// statusMetrics emits the raw engine state as info-style metrics: value 1, the
// state in the "status" label, one series per data center the domain is
// attached to. An unattached domain gets datacenter="" and the system-scope
// status ("unattached").
func (c *StorageDomainCollector) statusMetrics(d *StorageDomain, states []dcState, labelValues []string) []prometheus.Metric {
	if len(states) == 0 {
		status := d.Status
		if status == "" {
			status = "unknown"
		}
		l := append(append([]string{}, labelValues...), "", status)
		return []prometheus.Metric{metric.MustCreate(statusDesc, 1, l)}
	}

	ms := make([]prometheus.Metric, 0, len(states))
	for _, s := range states {
		status := s.status
		if status == "" {
			status = "unknown"
		}
		l := append(append([]string{}, labelValues...), s.dcName, status)
		ms = append(ms, metric.MustCreate(statusDesc, 1, l))
	}
	return ms
}

// statusToFloat maps a data-center-scoped StorageDomainStatus (active,
// activating, detaching, inactive, locked, maintenance, mixed,
// preparing_for_maintenance, unattached, unknown) to the ovirt_storage_up value.
func statusToFloat(status string) float64 {
	switch {
	case status == "active":
		return 1
	case maintenanceRegex.MatchString(status):
		return 2
	default:
		return 0
	}
}

// worst picks the more alarming of two ovirt_storage_up values: 0 (down) beats
// 2 (maintenance) beats 1 (active).
func worst(a, b float64) float64 {
	rank := func(v float64) int {
		switch v {
		case 0:
			return 2
		case 2:
			return 1
		default:
			return 0
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}

	return 0
}

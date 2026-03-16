// SPDX-License-Identifier: MIT

package datacenter

import (
	"context"

	"github.com/czerwonk/ovirt_exporter/pkg/collector"
	"github.com/czerwonk/ovirt_exporter/pkg/metric"
	"github.com/prometheus/client_golang/prometheus"
)

const prefix = "ovirt_datacenter_"

var (
	upDesc        *prometheus.Desc
	quotaModeDesc *prometheus.Desc
	localDesc     *prometheus.Desc
)

func init() {
	l := []string{"name"}
	upDesc        = prometheus.NewDesc(prefix+"up", "Data center status: up (1), maintenance (2), or down (0)", l, nil)
	quotaModeDesc = prometheus.NewDesc(prefix+"quota_mode", "Quota mode: disabled (0), audit (1), enforced (2)", l, nil)
	localDesc     = prometheus.NewDesc(prefix+"local", "Storage type is local (1) or shared (0)", l, nil)
}

// DataCenterCollector collects data center statistics from oVirt
type DataCenterCollector struct {
	cc              *collector.CollectorContext
	collectDuration prometheus.Observer
	rootCtx         context.Context
}

// NewCollector creates a new collector
func NewCollector(ctx context.Context, cc *collector.CollectorContext, collectDuration prometheus.Observer) prometheus.Collector {
	return &DataCenterCollector{
		rootCtx:         ctx,
		cc:              cc,
		collectDuration: collectDuration,
	}
}

// Collect implements Prometheus Collector interface
func (c *DataCenterCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, span := c.cc.Tracer().Start(c.rootCtx, "DataCenterCollector.Collect")
	defer span.End()

	c.cc.SetMetricsCh(ch)

	timer := prometheus.NewTimer(c.collectDuration)
	defer timer.ObserveDuration()

	dcs := DataCenters{}
	err := c.cc.Client().GetAndParse(ctx, "datacenters", &dcs)
	if err != nil {
		c.cc.HandleError(err, span)
		return
	}

	for _, dc := range dcs.DataCenters {
		l := []string{dc.Name}
		c.cc.RecordMetrics(
			metric.MustCreate(upDesc, statusToFloat(dc.Status), l),
			metric.MustCreate(quotaModeDesc, quotaModeToFloat(dc.QuotaMode), l),
			metric.MustCreate(localDesc, boolToFloat(dc.Local), l),
		)
	}
}

// Describe implements Prometheus Collector interface
func (c *DataCenterCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- upDesc
	ch <- quotaModeDesc
	ch <- localDesc
}

func statusToFloat(status string) float64 {
	switch status {
	case "up":
		return 1
	case "maintenance":
		return 2
	default:
		return 0
	}
}

func quotaModeToFloat(mode string) float64 {
	switch mode {
	case "audit":
		return 1
	case "enabled":
		return 2
	default:
		return 0
	}
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

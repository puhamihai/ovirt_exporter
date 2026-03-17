// SPDX-License-Identifier: MIT

package cluster

import (
	"context"

	"github.com/czerwonk/ovirt_exporter/pkg/collector"
	"github.com/czerwonk/ovirt_exporter/pkg/datacenter"
	"github.com/czerwonk/ovirt_exporter/pkg/metric"
	"github.com/prometheus/client_golang/prometheus"
)

const prefix = "ovirt_cluster_"

var (
	upDesc               *prometheus.Desc
	versionMajorDesc     *prometheus.Desc
	versionMinorDesc     *prometheus.Desc
	ballooningDesc       *prometheus.Desc
	ksmDesc              *prometheus.Desc
	memoryOvercommitDesc *prometheus.Desc
	fencingDesc          *prometheus.Desc
	haReservationDesc    *prometheus.Desc
	upgradeInProgressDesc *prometheus.Desc
)

func init() {
	l := []string{"name", "datacenter"}
	upDesc                = prometheus.NewDesc(prefix+"up", "Cluster data center status: up (1), maintenance (2), or down (0)", l, nil)
	versionMajorDesc      = prometheus.NewDesc(prefix+"version_major", "Cluster compatibility version major number", l, nil)
	versionMinorDesc      = prometheus.NewDesc(prefix+"version_minor", "Cluster compatibility version minor number", l, nil)
	ballooningDesc        = prometheus.NewDesc(prefix+"ballooning_enabled", "Memory ballooning is enabled (1) or not (0)", l, nil)
	ksmDesc               = prometheus.NewDesc(prefix+"ksm_enabled", "Kernel Same-page Merging (KSM) is enabled (1) or not (0)", l, nil)
	memoryOvercommitDesc  = prometheus.NewDesc(prefix+"memory_overcommit_percent", "Memory overcommit percentage configured for the cluster", l, nil)
	fencingDesc           = prometheus.NewDesc(prefix+"fencing_enabled", "Host fencing is enabled (1) or not (0)", l, nil)
	haReservationDesc     = prometheus.NewDesc(prefix+"ha_reservation", "HA reservation is enabled (1) or not (0) — when 0, VM failover may be impossible", l, nil)
	upgradeInProgressDesc = prometheus.NewDesc(prefix+"upgrade_in_progress", "Cluster upgrade is in progress (1) or not (0)", l, nil)
}

// ClusterCollector collects cluster statistics from oVirt
type ClusterCollector struct {
	cc              *collector.CollectorContext
	collectDuration prometheus.Observer
	rootCtx         context.Context
}

// NewCollector creates a new collector
func NewCollector(ctx context.Context, cc *collector.CollectorContext, collectDuration prometheus.Observer) prometheus.Collector {
	return &ClusterCollector{
		rootCtx:         ctx,
		cc:              cc,
		collectDuration: collectDuration,
	}
}

// Collect implements Prometheus Collector interface
func (c *ClusterCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, span := c.cc.Tracer().Start(c.rootCtx, "ClusterCollector.Collect")
	defer span.End()

	c.cc.SetMetricsCh(ch)

	timer := prometheus.NewTimer(c.collectDuration)
	defer timer.ObserveDuration()

	clusters := Clusters{}
	err := c.cc.Client().GetAndParse(ctx, "clusters", &clusters)
	if err != nil {
		c.cc.HandleError(err, span)
		return
	}

	for _, cl := range clusters.Clusters {
		dcName := datacenter.Name(ctx, cl.DataCenter.ID, c.cc.Client())
		dcStatus := datacenter.Status(ctx, cl.DataCenter.ID, c.cc.Client())
		l := []string{cl.Name, dcName}

		c.cc.RecordMetrics(
			metric.MustCreate(upDesc, dcStatusToFloat(dcStatus), l),
			metric.MustCreate(versionMajorDesc, float64(cl.Version.Major), l),
			metric.MustCreate(versionMinorDesc, float64(cl.Version.Minor), l),
			metric.MustCreate(ballooningDesc, boolToFloat(cl.BallooningEnabled), l),
			metric.MustCreate(ksmDesc, boolToFloat(cl.KSM.Enabled), l),
			metric.MustCreate(memoryOvercommitDesc, float64(cl.MemoryPolicy.OverCommit.Percent), l),
			metric.MustCreate(fencingDesc, boolToFloat(cl.FencingPolicy.Enabled), l),
			metric.MustCreate(haReservationDesc, boolToFloat(cl.HAReservation), l),
			metric.MustCreate(upgradeInProgressDesc, boolToFloat(cl.UpgradeInProgress), l),
		)
	}
}

// Describe implements Prometheus Collector interface
func (c *ClusterCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- upDesc
	ch <- versionMajorDesc
	ch <- versionMinorDesc
	ch <- ballooningDesc
	ch <- ksmDesc
	ch <- memoryOvercommitDesc
	ch <- fencingDesc
	ch <- haReservationDesc
	ch <- upgradeInProgressDesc
}

func dcStatusToFloat(status string) float64 {
	switch status {
	case "up":
		return 1
	case "maintenance":
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

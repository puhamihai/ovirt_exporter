// SPDX-License-Identifier: MIT

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/czerwonk/ovirt_exporter/pkg/api"
	"github.com/czerwonk/ovirt_exporter/pkg/collector"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/trace/noop"
)

// fakeEngine serves two clusters in dc1, whose status the test can change
// between scrapes, and one cluster in dc2, whose lookup always fails.
func fakeEngine(t *testing.T, dc1Status *atomic.Value) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/ovirt-engine/sso/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "bearer"})
	})
	mux.HandleFunc("/ovirt-engine/api/", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch strings.TrimPrefix(r.URL.Path, "/ovirt-engine/api/") {
		case "clusters":
			_, _ = fmt.Fprint(w, `<clusters>
			  <cluster id="c1"><name>cluster1</name><data_center id="dc1"/><version><major>4</major><minor>7</minor></version></cluster>
			  <cluster id="c2"><name>cluster2</name><data_center id="dc1"/><version><major>4</major><minor>7</minor></version></cluster>
			  <cluster id="c3"><name>cluster3</name><data_center id="dc2"/><version><major>4</major><minor>7</minor></version></cluster>
			</clusters>`)
		case "datacenters/dc1":
			_, _ = fmt.Fprintf(w, `<data_center id="dc1"><name>DC-ONE</name><status>%s</status></data_center>`, dc1Status.Load())
		case "datacenters/dc2":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	return httptest.NewServer(mux), &requests
}

func scrape(t *testing.T, c prometheus.Collector) map[string]*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	out := make(map[string]*dto.MetricFamily, len(families))
	for _, f := range families {
		out[f.GetName()] = f
	}
	return out
}

// value of the series for the given cluster name; ok=false if absent
func clusterValue(f *dto.MetricFamily, name string) (float64, bool) {
	for _, m := range f.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "name" && lp.GetValue() == name {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

// ovirt_cluster_up must follow the data center's current status on every
// scrape. It used to come from a cache that was filled on the first scrape
// and never cleared, so a data center going down never reached the metric.
func TestUpFollowsDataCenterStatusAcrossScrapes(t *testing.T) {
	var dc1Status atomic.Value
	dc1Status.Store("up")
	srv, requests := fakeEngine(t, &dc1Status)
	defer srv.Close()

	client, err := api.NewClient(srv.URL+"/ovirt-engine/api/", "user@internal", "secret")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c := NewCollector(context.Background(), collector.NewContext(noop.NewTracerProvider().Tracer("test"), client), prometheus.ObserverFunc(func(float64) {}))

	for _, step := range []struct {
		status string
		want   float64
	}{
		{"up", 1},
		{"not_operational", 0},
		{"maintenance", 2},
		{"up", 1},
	} {
		dc1Status.Store(step.status)
		requests.Store(0)

		fam := scrape(t, c)
		for _, name := range []string{"cluster1", "cluster2"} {
			if got, ok := clusterValue(fam["ovirt_cluster_up"], name); !ok || got != step.want {
				t.Errorf("dc1 %s: ovirt_cluster_up{name=%q} = %v (present=%v), want %v", step.status, name, got, ok, step.want)
			}
		}

		// a failed data center lookup skips the cluster for this scrape: no
		// series at all, rather than series with datacenter=""
		if _, ok := clusterValue(fam["ovirt_cluster_up"], "cluster3"); ok {
			t.Errorf("dc1 %s: cluster3's data center lookup failed, ovirt_cluster_up must be absent", step.status)
		}
		if _, ok := clusterValue(fam["ovirt_cluster_version_major"], "cluster3"); ok {
			t.Errorf("dc1 %s: cluster3's data center lookup failed, ovirt_cluster_version_major must be absent", step.status)
		}
		if got, ok := clusterValue(fam["ovirt_cluster_version_major"], "cluster1"); !ok || got != 4 {
			t.Errorf("dc1 %s: ovirt_cluster_version_major{name=\"cluster1\"} = %v (present=%v), want 4", step.status, got, ok)
		}

		// one request for /clusters and one per distinct data center
		if got := requests.Load(); got != 3 {
			t.Errorf("dc1 %s: engine saw %d API requests, want 3", step.status, got)
		}
	}
}

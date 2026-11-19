// SPDX-License-Identifier: MIT

package storagedomain

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

// fakeEngine replays the two listings of a 4.5 engine: the system-scope
// GET /storagedomains (no status for attached domains, external_status "ok"
// everywhere, "unattached" for the glance repository) and the data-center-scope
// GET /datacenters/{id}/storagedomains, which carries the real state.
// The response shapes follow an oVirt 4.5 engine.
func fakeEngine(t *testing.T, dcListings map[string]string, failDC map[string]int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/ovirt-engine/sso/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "bearer"})
	})
	mux.HandleFunc("/ovirt-engine/api/", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		path := strings.TrimPrefix(r.URL.Path, "/ovirt-engine/api/")
		switch {
		case path == "storagedomains":
			_, _ = fmt.Fprint(w, `<storage_domains>
			  <storage_domain id="sd-hosted"><name>hosted_storage</name><external_status>ok</external_status><master>true</master><storage><path></path><type>iscsi</type></storage><type>data</type><available>100</available><used>50</used><committed>60</committed><data_centers><data_center id="dc1"/></data_centers></storage_domain>
			  <storage_domain id="sd-1"><name>data1</name><external_status>ok</external_status><master>false</master><storage><type>iscsi</type></storage><type>data</type><data_centers><data_center id="dc1"/></data_centers></storage_domain>
			  <storage_domain id="sd-2"><name>data2</name><external_status>ok</external_status><master>false</master><storage><type>iscsi</type></storage><type>data</type><data_centers><data_center id="dc1"/></data_centers></storage_domain>
			  <storage_domain id="sd-tmpl"><name>data_templates</name><external_status>ok</external_status><master>false</master><storage><type>iscsi</type></storage><type>data</type><data_centers><data_center id="dc1"/></data_centers></storage_domain>
			  <storage_domain id="sd-iso"><name>iso1</name><external_status>ok</external_status><master>false</master><storage><path>/srv/iso</path><type>nfs</type></storage><type>iso</type><data_centers><data_center id="dc1"/><data_center id="dc2"/></data_centers></storage_domain>
			  <storage_domain id="sd-glance"><name>ovirt-image-repository</name><external_status>ok</external_status><status>unattached</status><storage><type>glance</type></storage><type>image</type></storage_domain>
			</storage_domains>`)
		case path == "datacenters":
			_, _ = fmt.Fprint(w, `<data_centers>
			  <data_center id="dc1"><name>DC-ONE</name><status>up</status></data_center>
			  <data_center id="dc2"><name>DC-TWO</name><status>up</status></data_center>
			</data_centers>`)
		case strings.HasPrefix(path, "datacenters/") && strings.HasSuffix(path, "/storagedomains"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "datacenters/"), "/storagedomains")
			if code, ok := failDC[id]; ok {
				w.WriteHeader(code)
				return
			}
			body, ok := dcListings[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = fmt.Fprint(w, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	return httptest.NewServer(mux), &requests
}

func gather(t *testing.T, srv *httptest.Server) map[string]*dto.MetricFamily {
	t.Helper()

	client, err := api.NewClient(srv.URL+"/ovirt-engine/api/", "user@internal", "secret")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cc := collector.NewContext(noop.NewTracerProvider().Tracer("test"), client)
	c := NewCollector(context.Background(), cc, prometheus.ObserverFunc(func(float64) {}))

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

func label(m *dto.Metric, name string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	return ""
}

// value of the series whose labels match all the given pairs; -1 if absent
func series(f *dto.MetricFamily, want map[string]string) (float64, bool) {
	for _, m := range f.GetMetric() {
		ok := true
		for k, v := range want {
			if label(m, k) != v {
				ok = false
				break
			}
		}
		if ok {
			return m.GetGauge().GetValue(), true
		}
	}
	return -1, false
}

const dc1Listing = `<storage_domains>
  <storage_domain id="sd-hosted"><name>hosted_storage</name><external_status>ok</external_status><master>true</master><status>active</status><data_center id="dc1"/></storage_domain>
  <storage_domain id="sd-1"><name>data1</name><external_status>ok</external_status><status>inactive</status><data_center id="dc1"/></storage_domain>
  <storage_domain id="sd-2"><name>data2</name><external_status>ok</external_status><status>active</status><data_center id="dc1"/></storage_domain>
  <storage_domain id="sd-tmpl"><name>data_templates</name><external_status>ok</external_status><status>maintenance</status><data_center id="dc1"/></storage_domain>
  <storage_domain id="sd-iso"><name>iso1</name><external_status>ok</external_status><status>active</status><data_center id="dc1"/></storage_domain>
</storage_domains>`

const dc2Listing = `<storage_domains>
  <storage_domain id="sd-iso"><name>iso1</name><external_status>ok</external_status><status>inactive</status><data_center id="dc2"/></storage_domain>
</storage_domains>`

// A data domain deactivated by the engine (LUN not visible), the template domain in maintenance, everything else active, while
// external_status is "ok" for all of them.
func TestUpFollowsDataCenterStatusNotExternalStatus(t *testing.T) {
	srv, requests := fakeEngine(t, map[string]string{"dc1": dc1Listing, "dc2": dc2Listing}, nil)
	defer srv.Close()

	fam := gather(t, srv)
	up := fam["ovirt_storage_up"]
	if up == nil {
		t.Fatal("ovirt_storage_up missing")
	}

	for name, want := range map[string]float64{
		"hosted_storage": 1, // active
		"data1":          0, // inactive: reported as 1 from external_status before
		"data2":          1,
		"data_templates": 2, // maintenance
		"iso1":           0, // active in dc1, inactive in dc2: worst wins
	} {
		got, ok := series(up, map[string]string{"name": name})
		if !ok {
			t.Errorf("%s: no ovirt_storage_up series", name)
			continue
		}
		if got != want {
			t.Errorf("ovirt_storage_up{name=%q} = %v, want %v", name, got, want)
		}
	}

	// unattached glance repository keeps the external_status encoding
	if got, ok := series(up, map[string]string{"name": "ovirt-image-repository", "type": "image"}); !ok || got != 1 {
		t.Errorf("unattached domain: up = %v (present=%v), want 1", got, ok)
	}

	// one request for /storagedomains, one for /datacenters, one per data center
	if got := requests.Load(); got != 4 {
		t.Errorf("engine saw %d API requests, want 4", got)
	}
}

func TestStatusMetricPerDataCenter(t *testing.T) {
	srv, _ := fakeEngine(t, map[string]string{"dc1": dc1Listing, "dc2": dc2Listing}, nil)
	defer srv.Close()

	st := gather(t, srv)["ovirt_storage_status"]
	if st == nil {
		t.Fatal("ovirt_storage_status missing")
	}

	cases := []map[string]string{
		{"name": "data1", "datacenter": "DC-ONE", "status": "inactive"},
		{"name": "data_templates", "datacenter": "DC-ONE", "status": "maintenance"},
		{"name": "iso1", "datacenter": "DC-ONE", "status": "active"},
		{"name": "iso1", "datacenter": "DC-TWO", "status": "inactive"},
		{"name": "ovirt-image-repository", "datacenter": "", "status": "unattached"},
	}
	for _, want := range cases {
		if got, ok := series(st, want); !ok || got != 1 {
			t.Errorf("ovirt_storage_status%v = %v (present=%v), want 1", want, got, ok)
		}
	}

	// exactly one series per (domain, data center) pair: 5 attached-in-dc1 + 1 in dc2 + 1 unattached
	if got := len(st.GetMetric()); got != 7 {
		t.Errorf("ovirt_storage_status has %d series, want 7", got)
	}

	// labels of ovirt_storage_up are unchanged (rules match on name/type/path)
	for _, m := range gather(t, srv)["ovirt_storage_up"].GetMetric() {
		var names []string
		for _, lp := range m.GetLabel() {
			names = append(names, lp.GetName())
		}
		if got := strings.Join(names, ","); got != "name,path,type" {
			t.Errorf("ovirt_storage_up labels = %q, want name,path,type", got)
		}
	}
}

// When a data center's listing fails, the domains attached to it must not be
// reported as up from external_status; their status series are absent while the
// capacity series and the other data centers' domains are still emitted.
func TestFailedDataCenterListingLeavesStatusAbsent(t *testing.T) {
	srv, _ := fakeEngine(t, map[string]string{"dc1": dc1Listing}, map[string]int{"dc2": http.StatusInternalServerError})
	defer srv.Close()

	fam := gather(t, srv)
	up := fam["ovirt_storage_up"]

	if _, ok := series(up, map[string]string{"name": "iso1"}); ok {
		t.Error("iso1 is attached to the failed dc2: ovirt_storage_up must be absent")
	}
	if got, ok := series(up, map[string]string{"name": "data1"}); !ok || got != 0 {
		t.Errorf("data1 (dc1 only) up = %v present=%v, want 0", got, ok)
	}
	if _, ok := series(fam["ovirt_storage_available_bytes"], map[string]string{"name": "iso1"}); !ok {
		t.Error("capacity series must still be emitted for a domain with unknown status")
	}
	// the state that IS known (dc1) stays visible, the failed dc2 has no series
	if got, ok := series(fam["ovirt_storage_status"], map[string]string{"name": "iso1", "datacenter": "DC-ONE", "status": "active"}); !ok || got != 1 {
		t.Errorf("iso1 dc1 status series = %v present=%v, want 1", got, ok)
	}
	if _, ok := series(fam["ovirt_storage_status"], map[string]string{"name": "iso1", "datacenter": "DC-TWO"}); ok {
		t.Error("iso1 must have no status series for the failed dc2")
	}
}

func TestStatusToFloat(t *testing.T) {
	for status, want := range map[string]float64{
		"active": 1, "maintenance": 2, "preparing_for_maintenance": 2,
		"inactive": 0, "locked": 0, "unknown": 0, "activating": 0, "detaching": 0, "mixed": 0, "": 0,
	} {
		if got := statusToFloat(status); got != want {
			t.Errorf("statusToFloat(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestWorst(t *testing.T) {
	for _, tc := range []struct{ a, b, want float64 }{
		{1, 1, 1}, {1, 2, 2}, {2, 1, 2}, {1, 0, 0}, {0, 2, 0}, {2, 0, 0}, {0, 0, 0},
	} {
		if got := worst(tc.a, tc.b); got != tc.want {
			t.Errorf("worst(%v,%v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

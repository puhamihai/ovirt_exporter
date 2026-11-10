// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/czerwonk/ovirt_exporter/pkg/api"
	"github.com/czerwonk/ovirt_exporter/pkg/collector"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/trace/noop"
)

// Disk metrics must cost two API requests per scrape, whatever the number of
// VMs and disks: the VM listing with follow=disk_attachments and one GET /disks.
// No per-VM /diskattachments and no per-disk /disks/{id} requests may be sent.
func TestDiskMetricsUseTwoRequestsAndJoinOnDiskID(t *testing.T) {
	const numVMs = 40

	var (
		mu       sync.Mutex
		requests = map[string]int{}
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ovirt-engine/sso/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "bearer"})
	})
	mux.HandleFunc("/ovirt-engine/api/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/ovirt-engine/api/")
		mu.Lock()
		requests[path]++
		mu.Unlock()

		switch {
		case path == "vms":
			if r.URL.Query().Get("follow") != "disk_attachments" {
				t.Errorf("GET vms without follow=disk_attachments: %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, "<vms>")
			for i := 0; i < numVMs; i++ {
				// two disks on every VM; VM 0 also references a disk /disks does not know
				fmt.Fprintf(w, `<vm id="vm%d"><name>vm%d</name><status>up</status><host id="h1"/><cluster id="c1"/>
				  <disk_attachments>
				    <disk_attachment id="d%d-boot"><bootable>true</bootable><logical_name>/dev/sda</logical_name><disk id="d%d-boot"/></disk_attachment>
				    <disk_attachment id="d%d-data"><bootable>false</bootable><logical_name>/dev/sdb</logical_name><disk id="d%d-data"/></disk_attachment>`, i, i, i, i, i, i)
				if i == 0 {
					fmt.Fprint(w, `<disk_attachment id="ghost"><disk id="ghost"/></disk_attachment>`)
				}
				fmt.Fprint(w, `</disk_attachments></vm>`)
			}
			fmt.Fprint(w, "</vms>")
		case path == "disks":
			fmt.Fprint(w, "<disks>")
			for i := 0; i < numVMs; i++ {
				// boot: thin qcow2 at vdsm's 1.1x ceiling; data: raw preallocated
				fmt.Fprintf(w, `<disk id="d%d-boot"><name>boot%d</name><alias>boot%d</alias><provisioned_size>128849018880</provisioned_size><actual_size>141733920768</actual_size><total_size>141733920768</total_size><format>cow</format><sparse>true</sparse><storage_type>image</storage_type><storage_domains><storage_domain id="sd1"/></storage_domains></disk>`, i, i, i)
				fmt.Fprintf(w, `<disk id="d%d-data"><name>data%d</name><alias>data%d</alias><provisioned_size>107374182400</provisioned_size><actual_size>107374182400</actual_size><total_size>107374182400</total_size><format>raw</format><sparse>false</sparse><storage_type>image</storage_type><storage_domains><storage_domain id="sd1"/></storage_domains></disk>`, i, i, i)
			}
			// an unattached disk must not produce a series
			fmt.Fprint(w, `<disk id="orphan"><name>orphan</name><alias>orphan</alias><provisioned_size>1</provisioned_size><actual_size>1</actual_size><total_size>1</total_size><format>cow</format><sparse>true</sparse></disk>`)
			fmt.Fprint(w, "</disks>")
		case path == "storagedomains/sd1":
			fmt.Fprint(w, `<storage_domain id="sd1"><name>data1</name></storage_domain>`)
		case path == "clusters/c1":
			fmt.Fprint(w, `<cluster id="c1"><name>cluster1</name></cluster>`)
		case path == "hosts/h1":
			fmt.Fprint(w, `<host id="h1"><name>kvm01</name></host>`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := api.NewClient(srv.URL+"/ovirt-engine/api/", "prometheus@internal", "secret")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	cc := collector.NewContext(noop.NewTracerProvider().Tracer("test"), client)
	c := NewCollector(context.Background(), cc, false, false, false, true, prometheus.ObserverFunc(func(float64) {}))

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	byName := map[string]*dto.MetricFamily{}
	for _, f := range families {
		byName[f.GetName()] = f
	}
	for _, name := range []string{"ovirt_vm_disk_provisioned_size_bytes", "ovirt_vm_disk_actual_size_bytes", "ovirt_vm_disk_total_size_bytes"} {
		if got := len(byName[name].GetMetric()); got != 2*numVMs {
			t.Errorf("%s: %d series, want %d (two attached disks per VM, orphan and ghost excluded)", name, got, 2*numVMs)
		}
	}
	if got := len(byName["ovirt_vm_up"].GetMetric()); got != numVMs {
		t.Errorf("ovirt_vm_up: %d series, want %d", got, numVMs)
	}

	// labels and values of one boot disk
	var boot *dto.Metric
	for _, m := range byName["ovirt_vm_disk_actual_size_bytes"].GetMetric() {
		if label(m, "disk_id") == "d7-boot" {
			boot = m
		}
	}
	if boot == nil {
		t.Fatal("no series for disk d7-boot")
	}
	want := map[string]string{
		"name": "vm7", "host": "kvm01", "cluster": "cluster1",
		"disk_name": "boot7", "disk_alias": "boot7", "disk_logical_name": "/dev/sda",
		"storage_domain": "data1", "disk_id": "d7-boot", "disk_format": "cow", "disk_sparse": "true",
	}
	for k, v := range want {
		if got := label(boot, k); got != v {
			t.Errorf("label %s = %q, want %q", k, got, v)
		}
	}
	if got := boot.GetGauge().GetValue(); got != 141733920768 {
		t.Errorf("actual_size = %v, want 141733920768", got)
	}

	// request accounting: exactly one vms and one disks request, nothing per VM or per disk
	mu.Lock()
	defer mu.Unlock()
	if requests["vms"] != 1 || requests["disks"] != 1 {
		t.Errorf("requests vms=%d disks=%d, want 1 and 1", requests["vms"], requests["disks"])
	}
	for p, n := range requests {
		if strings.Contains(p, "diskattachments") || strings.HasPrefix(p, "disks/") {
			t.Errorf("per-object disk request sent: %s x%d", p, n)
		}
	}
	if requests["storagedomains/sd1"] != 1 {
		t.Errorf("storagedomains/sd1 requested %d times, want 1 (cached)", requests["storagedomains/sd1"])
	}
}

func label(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

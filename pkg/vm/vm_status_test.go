// SPDX-License-Identifier: MIT

package vm

import (
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// statusMetric must carry the engine's raw status verbatim in the "status"
// label, with a constant value of 1, so a rule can match "paused" (VM stopped
// by the engine on a storage error) or "down" (HA VM that failed to restart)
// instead of the lossy ovirt_vm_up encoding.
func TestStatusMetricCarriesRawStatus(t *testing.T) {
	c := &VMCollector{}

	for _, status := range []string{"up", "down", "paused", "not_responding", "migrating", "suspended", "unknown"} {
		m := c.statusMetric(&VM{Name: "vm1", Status: status}, []string{"vm1", "kvm01", "cluster1"})

		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("status=%q: write: %v", status, err)
		}

		if got := pb.GetGauge().GetValue(); got != 1 {
			t.Errorf("status=%q: value = %v, want 1", status, got)
		}

		var got string
		for _, lp := range pb.GetLabel() {
			if lp.GetName() == "status" {
				got = lp.GetValue()
			}
		}
		if got != status {
			t.Errorf("status label = %q, want %q", got, status)
		}
	}
}

// An empty status (engine omitted the element) must not produce an empty
// label value, which Prometheus treats as "label absent".
func TestStatusMetricEmptyStatusIsUnknown(t *testing.T) {
	c := &VMCollector{}
	m := c.statusMetric(&VM{Name: "vm1"}, []string{"vm1", "", "cluster1"})

	var pb dto.Metric
	if err := m.Write(&pb); err != nil {
		t.Fatal(err)
	}
	for _, lp := range pb.GetLabel() {
		if lp.GetName() == "status" && lp.GetValue() != "unknown" {
			t.Errorf("status label = %q, want \"unknown\"", lp.GetValue())
		}
	}
}

// ovirt_vm_up keeps its encoding: only "up" is 1.
func TestUpMetricEncodingUnchanged(t *testing.T) {
	c := &VMCollector{}

	for status, want := range map[string]float64{"up": 1, "paused": 0, "down": 0, "migrating": 0, "not_responding": 0} {
		m := c.upMetric(&VM{Name: "vm1", Status: status}, []string{"vm1", "kvm01", "cluster1"})

		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("status=%q: write: %v", status, err)
		}
		if got := pb.GetGauge().GetValue(); got != want {
			t.Errorf("up(%q) = %v, want %v", status, got, want)
		}
	}
}

// statusLabelNames is built from a copy; the shared labelNames slice must be
// untouched (the disk label set is appended to it elsewhere in init).
func TestStatusLabelNamesNotMutatingLabelNames(t *testing.T) {
	if got := strings.Join(labelNames, ","); got != "name,host,cluster" {
		t.Errorf("labelNames = %q, want \"name,host,cluster\"", got)
	}
	if got := strings.Join(statusLabelNames, ","); got != "name,host,cluster,status" {
		t.Errorf("statusLabelNames = %q, want \"name,host,cluster,status\"", got)
	}
}

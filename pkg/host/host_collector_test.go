// SPDX-License-Identifier: MIT

package host

import (
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// statusMetric must carry the engine's raw status verbatim in the "status"
// label, with a constant value of 1, so alerts can match one specific state.
func TestStatusMetricCarriesRawStatus(t *testing.T) {
	c := &HostCollector{}

	for _, status := range []string{"unassigned", "non_responsive", "up", "maintenance", "install_failed"} {
		m := c.statusMetric(&Host{Name: "kvm01", Status: status}, []string{"kvm01", "cluster1"})

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

// The lossy ovirt_host_up encoding is unchanged - this is the behaviour that
// made "unassigned" indistinguishable from "non_responsive" in the first place.
func TestUpMetricEncodingUnchanged(t *testing.T) {
	c := &HostCollector{}

	for status, want := range map[string]float64{
		"up": 1, "maintenance": 2, "installing": 2,
		"unassigned": 0, "non_responsive": 0, "down": 0,
	} {
		m := c.upMetric(&Host{Name: "kvm01", Status: status}, []string{"kvm01", "cluster1"})

		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("status=%q: write: %v", status, err)
		}
		if got := pb.GetGauge().GetValue(); got != want {
			t.Errorf("up(%q) = %v, want %v", status, got, want)
		}
	}
}

// statusLabelNames is built with a copy, so the shared labelNames slice used by
// every other host metric must be untouched.
func TestLabelNamesNotMutated(t *testing.T) {
	if got := strings.Join(labelNames, ","); got != "name,cluster" {
		t.Errorf("labelNames = %q, want \"name,cluster\"", got)
	}
	if got := strings.Join(statusLabelNames, ","); got != "name,cluster,status" {
		t.Errorf("statusLabelNames = %q, want \"name,cluster,status\"", got)
	}
}

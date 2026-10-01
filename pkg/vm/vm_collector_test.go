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
	"sync/atomic"
	"testing"
	"time"

	"github.com/czerwonk/ovirt_exporter/pkg/api"
	"github.com/czerwonk/ovirt_exporter/pkg/collector"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace/noop"
)

// Replays the 01-Oct-2026 incident against the real collector and client: a
// 163-VM engine whose session is invalidated in the middle of the per-VM
// snapshot fan-out. The engine must see at most the configured number of
// requests in flight, exactly one re-login, and the exporter must still
// produce every snapshot metric.
func TestSnapshotFanOutIsBoundedAndReauthenticatesOnce(t *testing.T) {
	const (
		numVMs = 163
		limit  = 8
	)

	var (
		mu        sync.Mutex
		token     string
		seq       int
		logins    atomic.Int32
		inFlight  atomic.Int32
		maxFlight atomic.Int32
		expireOne sync.Once
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/ovirt-engine/sso/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		seq++
		token = fmt.Sprintf("token-%d", seq)
		tok := token
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"access_token": tok, "token_type": "bearer"})
	})
	mux.HandleFunc("/ovirt-engine/api/", func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := maxFlight.Load()
			if n <= m || maxFlight.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)

		path := strings.TrimPrefix(r.URL.Path, "/ovirt-engine/api/")
		if strings.HasSuffix(path, "/snapshots") {
			// the session dies as the snapshot fan-out starts
			expireOne.Do(func() {
				mu.Lock()
				token = "expired"
				mu.Unlock()
			})
		}

		mu.Lock()
		ok := r.Header.Get("Authorization") == "Bearer "+token
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch {
		case path == "vms":
			fmt.Fprint(w, "<vms>")
			for i := 0; i < numVMs; i++ {
				fmt.Fprintf(w, `<vm id="vm%d"><name>vm%d</name><status>up</status><host id="h1"/><cluster id="c1"/></vm>`, i, i)
			}
			fmt.Fprint(w, "</vms>")
		case strings.HasSuffix(path, "/snapshots"):
			fmt.Fprint(w, `<snapshots>
				<snapshot id="active"><date>2026-10-01T07:00:00Z</date></snapshot>
				<snapshot id="s1"><date>2026-09-01T07:00:00Z</date></snapshot>
			</snapshots>`)
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

	client, err := api.NewClient(srv.URL+"/ovirt-engine/api/", "prometheus@internal", "secret", api.WithMaxConcurrentRequests(limit))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	cc := collector.NewContext(noop.NewTracerProvider().Tracer("test"), client)
	c := NewCollector(context.Background(), cc, false, true, false, false, prometheus.ObserverFunc(func(float64) {}))

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	counts := map[string]int{}
	for _, f := range families {
		counts[f.GetName()] = len(f.GetMetric())
	}
	for _, name := range []string{"ovirt_vm_up", "ovirt_vm_snapshots", "ovirt_vm_snapshot_max_age_seconds", "ovirt_vm_snapshot_min_age_seconds"} {
		if counts[name] != numVMs {
			t.Errorf("%s: %d series, want %d", name, counts[name], numVMs)
		}
	}

	if got := maxFlight.Load(); got > limit {
		t.Errorf("max requests in flight = %d, want <= %d", got, limit)
	}
	if got := logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2 (initial + exactly one re-login)", got)
	}
}

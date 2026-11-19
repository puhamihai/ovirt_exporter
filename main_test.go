// SPDX-License-Identifier: MIT

package main

import (
	rtdebug "runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	stamped := func(v string) *rtdebug.BuildInfo {
		return &rtdebug.BuildInfo{Main: rtdebug.Module{Path: "github.com/czerwonk/ovirt_exporter", Version: v}}
	}

	for _, tc := range []struct {
		name     string
		injected string
		bi       *rtdebug.BuildInfo
		ok       bool
		want     string
	}{
		{"injected wins over build info", "1.2.3", stamped("v9.9.9"), true, "1.2.3"},
		{"tagged build without -X", "dev", stamped("v1.2.3"), true, "1.2.3"},
		{"untagged build keeps the pseudo-version", "dev", stamped("v1.2.4-0.20260101000000-abcdef123456+dirty"), true, "1.2.4-0.20260101000000-abcdef123456+dirty"},
		{"go run reports (devel)", "dev", stamped("(devel)"), true, "dev"},
		{"no build info", "dev", nil, false, "dev"},
		{"empty injected value", "", stamped(""), true, "dev"},
	} {
		if got := resolveVersion(tc.injected, tc.bi, tc.ok); got != tc.want {
			t.Errorf("%s: resolveVersion(%q) = %q, want %q", tc.name, tc.injected, got, tc.want)
		}
	}
}

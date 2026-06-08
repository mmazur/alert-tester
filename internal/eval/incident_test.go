package eval

import (
	"testing"
	"time"

	"alert-tester/internal/model"
)

func TestMergeFiringsGap(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	firings := []model.FiringRange{
		{FirstFired: t0, LastFired: t0.Add(5 * time.Minute), MaxValue: 1},
		{FirstFired: t0.Add(10 * time.Minute), LastFired: t0.Add(12 * time.Minute), MaxValue: 3},
		{FirstFired: t0.Add(30 * time.Minute), LastFired: t0.Add(31 * time.Minute), MaxValue: 2},
	}

	got := MergeFirings(firings, 10*time.Minute)
	if len(got) != 2 {
		t.Fatalf("MergeFirings returned %v, want 2 firings", got)
	}
	if got[0].LastFired != t0.Add(12*time.Minute) || got[0].MaxValue != 3 {
		t.Fatalf("first merged firing = %+v, want extended range and max value", got[0])
	}
}

func TestCorrelatedFiringsMultiSeriesResolution(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	min := time.Minute

	// Two clusters, 7 series, 7 raw firings.
	// Grouped by cluster with mergeGap=5m → 3 correlated firings:
	//
	// cluster=alpha (4 series, 2 fire/resolve cycles separated by 12m > 5m):
	//   Cycle 1: pods a,b,c fire overlapping 0–10m → merged: 0–10m
	//   Cycle 2: pod d fires alone at 22–28m → merged: 22–28m
	//
	// cluster=beta (3 series, 1 fire/resolve cycle):
	//   pods e,f,g fire overlapping 1–12m → merged: 1–12m
	results := []model.AlertResult{
		// alpha cycle 1
		{LabelSet: map[string]string{"cluster": "alpha", "pod": "a"}, Fired: true, Firings: []model.FiringRange{
			{FirstFired: t0, LastFired: t0.Add(5 * min)},
		}},
		{LabelSet: map[string]string{"cluster": "alpha", "pod": "b"}, Fired: true, Firings: []model.FiringRange{
			{FirstFired: t0.Add(2 * min), LastFired: t0.Add(8 * min)},
		}},
		{LabelSet: map[string]string{"cluster": "alpha", "pod": "c"}, Fired: true, Firings: []model.FiringRange{
			{FirstFired: t0.Add(4 * min), LastFired: t0.Add(10 * min)},
		}},
		// alpha cycle 2 (12m gap from cycle 1's end at 10m)
		{LabelSet: map[string]string{"cluster": "alpha", "pod": "d"}, Fired: true, Firings: []model.FiringRange{
			{FirstFired: t0.Add(22 * min), LastFired: t0.Add(28 * min)},
		}},
		// beta single cycle
		{LabelSet: map[string]string{"cluster": "beta", "pod": "e"}, Fired: true, Firings: []model.FiringRange{
			{FirstFired: t0.Add(1 * min), LastFired: t0.Add(6 * min)},
		}},
		{LabelSet: map[string]string{"cluster": "beta", "pod": "f"}, Fired: true, Firings: []model.FiringRange{
			{FirstFired: t0.Add(3 * min), LastFired: t0.Add(9 * min)},
		}},
		{LabelSet: map[string]string{"cluster": "beta", "pod": "g"}, Fired: true, Firings: []model.FiringRange{
			{FirstFired: t0.Add(7 * min), LastFired: t0.Add(12 * min)},
		}},
	}

	// Raw firings (how the real code counts TotalFirings): sum of len(r.Firings) per series
	totalRaw := 0
	for _, r := range results {
		totalRaw += len(r.Firings)
	}
	if totalRaw != 7 {
		t.Fatalf("total raw firings = %d, want 7", totalRaw)
	}

	// Grouped by cluster: alpha→2 cycles, beta→1 cycle = 3 total
	grouped := CorrelatedFirings(results, []string{"cluster"}, 5*min)
	if grouped != 3 {
		t.Fatalf("CorrelatedFirings grouped = %d, want 3 (alpha:2 + beta:1)", grouped)
	}
}

func TestCorrelatedFiringsAndGroupIncidents(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	results := []model.AlertResult{
		{
			LabelSet: map[string]string{"cluster": "foo", "instance": "a"},
			Fired:    true,
			Firings: []model.FiringRange{
				{FirstPending: t0, FirstFired: t0.Add(time.Minute), LastFired: t0.Add(2 * time.Minute)},
			},
		},
		{
			LabelSet: map[string]string{"cluster": "foo", "instance": "b"},
			Fired:    true,
			Firings: []model.FiringRange{
				{FirstPending: t0.Add(time.Minute), FirstFired: t0.Add(2 * time.Minute), LastFired: t0.Add(3 * time.Minute)},
			},
		},
		{
			LabelSet: map[string]string{"cluster": "bar", "instance": "c"},
			Fired:    true,
			Firings: []model.FiringRange{
				{FirstPending: t0.Add(10 * time.Minute), FirstFired: t0.Add(11 * time.Minute), LastFired: t0.Add(12 * time.Minute)},
			},
		},
	}

	if got := CorrelatedFirings(results, []string{"cluster"}, 0); got != 2 {
		t.Fatalf("CorrelatedFirings = %d, want 2", got)
	}

	incidents := GroupIncidents(results, []string{"cluster"})
	if len(incidents) != 2 {
		t.Fatalf("GroupIncidents returned %v, want 2 incidents", incidents)
	}
	if incidents[0].CorrelationKey != "{cluster=foo}" {
		t.Fatalf("first incident key = %q, want foo first by pending time", incidents[0].CorrelationKey)
	}
	if len(incidents[0].Firings) != 2 {
		t.Fatalf("foo incident firings = %d, want 2", len(incidents[0].Firings))
	}
}

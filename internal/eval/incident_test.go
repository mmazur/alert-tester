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

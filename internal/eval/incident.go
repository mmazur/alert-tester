package eval

import (
	"sort"
	"strings"
	"time"

	"alert-tester/internal/model"
)

// CorrelatedFirings groups firings by correlation key and merges overlapping
// or adjacent firings on the same key into single firings. With mergeGap=0
// only actually overlapping firings merge; pass a positive gap to also merge
// firings separated by less than that duration (resolution-delay hysteresis).
func CorrelatedFirings(results []model.AlertResult, correlationLabels []string, mergeGap time.Duration) int {
	byKey := make(map[string][]model.FiringRange)
	for _, r := range results {
		if len(r.Firings) == 0 {
			continue
		}
		key := resolveCorrelationKey(r.LabelSet, correlationLabels)
		byKey[key] = append(byKey[key], r.Firings...)
	}

	total := 0
	for _, firings := range byKey {
		total += len(MergeFirings(firings, mergeGap))
	}
	return total
}

// MergeFirings sorts firings by FirstFired and collapses any pair whose gap
// (FirstFired of next minus LastFired of previous) is <= mergeGap into a
// single firing spanning both. With mergeGap=0 only true overlaps merge.
func MergeFirings(firings []model.FiringRange, mergeGap time.Duration) []model.FiringRange {
	if len(firings) == 0 {
		return nil
	}
	sort.Slice(firings, func(i, j int) bool {
		return firings[i].FirstFired.Before(firings[j].FirstFired)
	})
	merged := []model.FiringRange{firings[0]}
	for _, f := range firings[1:] {
		last := &merged[len(merged)-1]
		gap := f.FirstFired.Sub(last.LastFired)
		if gap <= mergeGap {
			if f.LastFired.After(last.LastFired) {
				last.LastFired = f.LastFired
			}
			if f.MaxValue > last.MaxValue {
				last.MaxValue = f.MaxValue
			}
		} else {
			merged = append(merged, f)
		}
	}
	return merged
}

func GroupIncidents(results []model.AlertResult, correlationLabels []string) []model.Incident {
	incidentMap := make(map[string]*model.Incident)

	for _, r := range results {
		if !r.Fired {
			continue
		}

		key := resolveCorrelationKey(r.LabelSet, correlationLabels)

		inc, ok := incidentMap[key]
		if !ok {
			labels := make(map[string]string)
			for _, l := range correlationLabels {
				if v, exists := r.LabelSet[l]; exists {
					labels[l] = v
				}
			}
			inc = &model.Incident{
				CorrelationKey: key,
				Labels:         labels,
			}
			incidentMap[key] = inc
		}

		inc.Firings = append(inc.Firings, r.Firings...)
	}

	var incidents []model.Incident
	for _, inc := range incidentMap {
		sort.Slice(inc.Firings, func(i, j int) bool {
			return inc.Firings[i].FirstPending.Before(inc.Firings[j].FirstPending)
		})
		if len(inc.Firings) > 0 {
			inc.FirstPending = inc.Firings[0].FirstPending
			inc.LastFired = inc.Firings[len(inc.Firings)-1].LastFired
			for _, f := range inc.Firings {
				if f.LastFired.After(inc.LastFired) {
					inc.LastFired = f.LastFired
				}
			}
		}
		incidents = append(incidents, *inc)
	}

	sort.Slice(incidents, func(i, j int) bool {
		return incidents[i].FirstPending.Before(incidents[j].FirstPending)
	})

	return incidents
}

func resolveCorrelationKey(labels map[string]string, correlationLabels []string) string {
	if len(correlationLabels) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(correlationLabels))
	for _, l := range correlationLabels {
		v := labels[l]
		parts = append(parts, l+"="+v)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

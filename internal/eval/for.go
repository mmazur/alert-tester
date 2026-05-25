package eval

import (
	"math"
	"sort"
	"time"

	"alert-tester/internal/model"
)

func EvaluateFor(series []model.Series, forDuration, evalInterval time.Duration) []model.AlertResult {
	var results []model.AlertResult

	for _, s := range series {
		result := model.AlertResult{
			For:      forDuration,
			LabelSet: s.Labels,
		}

		aligned := subsample(s.Samples, evalInterval)
		if len(aligned) == 0 {
			results = append(results, result)
			continue
		}

		result.Firings = findFirings(aligned, forDuration)
		result.Fired = len(result.Firings) > 0

		results = append(results, result)
	}

	return results
}

func subsample(samples []model.Sample, interval time.Duration) []model.Sample {
	if len(samples) == 0 || interval <= 0 {
		return samples
	}

	sort.Slice(samples, func(i, j int) bool {
		return samples[i].Timestamp.Before(samples[j].Timestamp)
	})

	intervalSec := int64(interval.Seconds())
	firstTS := samples[0].Timestamp.Unix()
	alignedFirst := firstTS - (firstTS % intervalSec)

	sampleIdx := 0
	var result []model.Sample

	for ts := alignedFirst; ; ts += intervalSec {
		t := time.Unix(ts, 0).UTC()
		if t.After(samples[len(samples)-1].Timestamp) {
			break
		}
		if t.Before(samples[0].Timestamp) {
			continue
		}

		// Find the closest sample to this aligned timestamp
		for sampleIdx < len(samples)-1 && samples[sampleIdx+1].Timestamp.Unix() <= ts {
			sampleIdx++
		}

		// If the closest sample is reasonably close, use its value; otherwise
		// emit an explicit "absent" tick (NaN) so that findFirings treats the
		// gap as resolution. This matches Alertmanager behavior, where a
		// filtering expression like `x > threshold` produces no sample when the
		// condition is false, and that absence resolves the alert.
		if abs(samples[sampleIdx].Timestamp.Unix()-ts) <= intervalSec {
			result = append(result, model.Sample{
				Timestamp: t,
				Value:     samples[sampleIdx].Value,
			})
		} else {
			result = append(result, model.Sample{
				Timestamp: t,
				Value:     math.NaN(),
			})
		}
	}

	return result
}

func findFirings(samples []model.Sample, forDuration time.Duration) []model.FiringRange {
	if len(samples) == 0 {
		return nil
	}

	var firings []model.FiringRange
	var runStart time.Time
	var maxVal float64
	inRun := false

	for _, s := range samples {
		nonZero := s.Value != 0 && !math.IsNaN(s.Value)

		if nonZero {
			if !inRun {
				runStart = s.Timestamp
				maxVal = s.Value
				inRun = true
			} else if s.Value > maxVal {
				maxVal = s.Value
			}

			elapsed := s.Timestamp.Sub(runStart)
			if forDuration == 0 || elapsed >= forDuration {
				if len(firings) > 0 && firings[len(firings)-1].FirstPending == runStart {
					firings[len(firings)-1].LastFired = s.Timestamp
					firings[len(firings)-1].MaxValue = maxVal
				} else {
					f := model.FiringRange{
						FirstPending: runStart,
						FirstFired:   s.Timestamp,
						LastFired:    s.Timestamp,
						MaxValue:     maxVal,
					}
					if forDuration == 0 {
						f.FirstFired = runStart
					}
					firings = append(firings, f)
				}
			}
		} else {
			inRun = false
		}
	}

	return firings
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

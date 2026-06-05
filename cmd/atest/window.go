package main

import "time"

func maxForDuration(forDurations []time.Duration) time.Duration {
	maxFor := time.Duration(0)
	for _, d := range forDurations {
		if d > maxFor {
			maxFor = d
		}
	}
	return maxFor
}

func computePreroll(forDurations []time.Duration, delayResolutionBy, chunkSize time.Duration) time.Duration {
	prerollBase := maxForDuration(forDurations)
	if delayResolutionBy > prerollBase {
		prerollBase = delayResolutionBy
	}
	prerollBase += 10 * time.Minute
	preroll := alignUpToChunk(prerollBase, chunkSize)
	if preroll < 2*time.Hour {
		return 2 * time.Hour
	}
	return preroll
}

func deriveQueryFrom(from time.Time, preroll time.Duration) time.Time {
	return from.Add(-preroll)
}

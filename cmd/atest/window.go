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
	return alignUpToChunk(prerollBase, chunkSize)
}

func deriveQueryFrom(from time.Time, preroll time.Duration) time.Time {
	return from.Add(-preroll)
}

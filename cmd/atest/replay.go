package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"alert-tester/internal/buildinfo"

	"github.com/spf13/cobra"
)

const replaySchemaVersion = 1

var replayDefaults = replayConfig{
	ForDurations:      []time.Duration{0},
	EvalInterval:      time.Minute,
	DelayResolutionBy: 0,
	IncidentGroupBy:   "",
	ChunkSize:         time.Hour,
}

type replayConfig struct {
	ForDurations      []time.Duration
	EvalInterval      time.Duration
	DelayResolutionBy time.Duration
	IncidentGroupBy   string
	ChunkSize         time.Duration
}

func (c replayConfig) correlationLabels() []string {
	return parseCorrelationID(c.IncidentGroupBy)
}

func (c replayConfig) json() replayConfigJSON {
	return replayConfigJSON{
		For:               durationsJSON(c.ForDurations),
		EvalInterval:      trimDur(c.EvalInterval),
		DelayResolutionBy: trimDur(c.DelayResolutionBy),
		IncidentGroupBy:   c.IncidentGroupBy,
		ChunkSize:         trimDur(c.ChunkSize),
	}
}

func (c replayConfig) equal(other replayConfig) bool {
	if c.EvalInterval != other.EvalInterval || c.DelayResolutionBy != other.DelayResolutionBy || c.IncidentGroupBy != other.IncidentGroupBy || c.ChunkSize != other.ChunkSize {
		return false
	}
	if len(c.ForDurations) != len(other.ForDurations) {
		return false
	}
	for i := range c.ForDurations {
		if c.ForDurations[i] != other.ForDurations[i] {
			return false
		}
	}
	return true
}

func newReplayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Capture and compare local replay baselines from cached data",
	}
	cmd.AddCommand(newReplayCaptureCmd())
	return cmd
}

func defaultCacheDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".cache", "atest")
}

func projectRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fi, err := os.Stat(filepath.Join(wd, ".git")); err == nil && fi != nil {
			return wd, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", fmt.Errorf("could not find repository root from %s", wd)
		}
		wd = parent
	}
}

func replayRunDirName(now time.Time) string {
	info := buildinfo.Current()
	rev := info.Revision
	if len(rev) > 12 {
		rev = rev[:12]
	}
	rev = strings.TrimSpace(rev)
	if rev == "" {
		rev = "unknown"
	}
	return fmt.Sprintf("%s-%s-%s", now.UTC().Format("20060102150405"), rev, info.TreeState())
}

func durationsJSON(durations []time.Duration) []string {
	out := make([]string, len(durations))
	for i, d := range durations {
		out[i] = trimDur(d)
	}
	return out
}

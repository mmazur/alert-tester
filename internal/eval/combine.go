package eval

import (
	"math"
	"sort"
	"strings"

	"alert-tester/internal/model"
)

// CombineOp selects how two series sets are combined.
type CombineOp int

const (
	OpAnd CombineOp = iota
	OpOr
)

// Combine joins two filtered series sets with PromQL set-op semantics.
//
// Each clause's series is treated as "presence" data: a sample exists at
// timestamp T iff that label set is firing at T (callers should drop NaN
// samples first via NormalizeForCombine).
//
// Matching is on the full label set, like upstream PromQL with no
// on()/ignoring() modifier.
//
//	OpAnd: emit (L, T, value=left) where L is present in both sides at T.
//	OpOr:  emit (L, T) where L is present in either side at T; value comes
//	       from left when both have it, else from whichever side has it.
func Combine(left, right []model.Series, op CombineOp) []model.Series {
	li := indexSeries(left)
	ri := indexSeries(right)

	keys := make(map[string]struct{}, len(li)+len(ri))
	if op == OpAnd {
		for k := range li {
			if _, ok := ri[k]; ok {
				keys[k] = struct{}{}
			}
		}
	} else {
		for k := range li {
			keys[k] = struct{}{}
		}
		for k := range ri {
			keys[k] = struct{}{}
		}
	}

	out := make([]model.Series, 0, len(keys))
	for k := range keys {
		var s model.Series
		switch {
		case li[k] != nil && ri[k] != nil:
			s = combinePair(li[k], ri[k], op)
		case op == OpOr && li[k] != nil:
			s = *li[k]
		case op == OpOr && ri[k] != nil:
			s = *ri[k]
		}
		if len(s.Samples) == 0 {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return labelKey(out[i].Labels) < labelKey(out[j].Labels)
	})
	return out
}

// NormalizeForCombine drops NaN samples so that "sample exists at T"
// uniformly means "firing at T", regardless of whether the clause was filtered
// by a comparator flag (which already drops non-matching samples) or fetched
// raw. Zero-valued samples are preserved because Prometheus alerting is driven
// by vector presence, not by sample value being > 0.
func NormalizeForCombine(series []model.Series) []model.Series {
	out := make([]model.Series, 0, len(series))
	for _, s := range series {
		kept := make([]model.Sample, 0, len(s.Samples))
		for _, sm := range s.Samples {
			if math.IsNaN(sm.Value) {
				continue
			}
			kept = append(kept, sm)
		}
		if len(kept) == 0 {
			continue
		}
		out = append(out, model.Series{Labels: s.Labels, Samples: kept})
	}
	return out
}

func combinePair(left, right *model.Series, op CombineOp) model.Series {
	out := model.Series{Labels: left.Labels}
	li, ri := 0, 0
	ls, rs := left.Samples, right.Samples
	for li < len(ls) && ri < len(rs) {
		lt := ls[li].Timestamp.UnixNano()
		rt := rs[ri].Timestamp.UnixNano()
		switch {
		case lt == rt:
			out.Samples = append(out.Samples, ls[li])
			li++
			ri++
		case lt < rt:
			if op == OpOr {
				out.Samples = append(out.Samples, ls[li])
			}
			li++
		default:
			if op == OpOr {
				out.Samples = append(out.Samples, rs[ri])
			}
			ri++
		}
	}
	if op == OpOr {
		out.Samples = append(out.Samples, ls[li:]...)
		out.Samples = append(out.Samples, rs[ri:]...)
	}
	return out
}

func indexSeries(series []model.Series) map[string]*model.Series {
	idx := make(map[string]*model.Series, len(series))
	for i := range series {
		s := &series[i]
		sort.Slice(s.Samples, func(a, b int) bool {
			return s.Samples[a].Timestamp.Before(s.Samples[b].Timestamp)
		})
		idx[labelKey(s.Labels)] = s
	}
	return idx
}

func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(0x1f)
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}

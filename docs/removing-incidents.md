# Removing the "incidents" concept

## Why "incidents" as currently implemented are misleading

The `Incidents` count in atest output is the number of **distinct correlation keys** that
ever fired during the analysis window. With `--incident-group-by cluster`, it's just "how
many clusters fired at least once."

This is not what a user expects when they see the word "incident." In most typical
alerting systems, an incident is a fire/resolve cycle — an alert fires, stays active for
some duration, resolves, and if it fires again later that's a *new* incident. The current
`Incidents` count doesn't model resolution at all. A cluster that fires 50 times across 7
distinct fire/resolve cycles still shows as "1 incident."

## Why grouped firings are the real incident-equivalent

`GroupedFirings` does what users expect:

1. All firing ranges from all series sharing the same correlation key are pooled together.
2. Overlapping or adjacent ranges (within `--delay-resolution-by`) are merged.
3. After merge, each remaining range represents one fire/resolve cycle on that key.

So if cluster X fires 0-10m, resolves, then fires again 20-30m (gap > delay-resolution-by),
that's **2 grouped firings** — two incidents in the intuitive sense.

The merge correctly handles the multi-series case: if pod A fires 0-5m and pod B fires 3-8m
on the same cluster, they merge into one grouped firing (0-8m). The alert was continuously
active on that key for that span.

## The console output change

The output line now reads:

```
- for 5m: 28 firings, 7 grouped firings (~incidents)
```

The `(~incidents)` annotation tells users that grouped firings is the closest thing to an
incident count without needing to understand the internal distinction.

## What remains in the code

The `GroupIncidents()` function and `model.Incident` type still exist. They're used by:

- The verbose console output (`printIncidents`) to show per-key firing breakdowns
- The replay capture/compare JSON to track per-key changes between runs

These are useful for their per-key grouping, just not for the "N incidents" headline number.

## Future cleanup

If we want to remove the `Incident` type entirely:

1. Add `CorrelatedFiringsByKey()` that returns `map[string][]model.FiringRange` (same merge
   logic as `CorrelatedFirings` but returning the map instead of a total count).
2. Source `printIncidents` and the replay detail JSON from that map.
3. Delete `GroupIncidents()` and `model.Incident`.
4. The replay JSON field names (`incident_count`, `incidents`) can stay for backwards compat
   with existing replay files on disk — they're just populated differently.

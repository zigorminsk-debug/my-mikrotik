package mikrotik

import (
	"context"
	"strings"
	"unicode"
)

// The monitor response can contain IMEI, IMSI, ICCID and cell identifiers.
// Ask only for these fields, and also strictly filter the returned result.
const lteMonitorProperties = "status,registration-status,data-class,access-technology,session-uptime,rssi,rsrp,rsrq,sinr,cqi"
const maxLTEInterfaces = 4

type lteMonitorReader func(string) ([]map[string]string, error)

func safeLTEValue(s string) string {
	if len(s) > 128 {
		return ""
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return s
}

// collectLTE never retries a monitor call and never treats missing data as a
// failure or a healthy signal. API errors stop further requests on the session.
func collectLTE(ctx context.Context, read snapshotReader, monitor lteMonitorReader, stopOnError bool) SnapshotSection {
	section := SnapshotSection{Status: "skipped", Rows: []map[string]string{}}
	if ctx.Err() != nil {
		return section
	}
	recs, err := read("/interface/lte")
	if err != nil {
		section.Status = "error"
		return section
	}
	section.Status = "ok"
	if len(recs) > maxLTEInterfaces {
		section.Truncated = true
		recs = recs[:maxLTEInterfaces]
	}
	stopped := false
	for _, rec := range recs {
		row := map[string]string{}
		for _, k := range []string{"name", "disabled", "running"} {
			if v := safeLTEValue(rec[k]); v != "" {
				row[k] = v
			}
		}
		name := row["name"]
		switch {
		case ctx.Err() != nil || stopped:
			row["monitor-status"] = "skipped"
		case name == "" || explicitlyTrue(rec["disabled"]):
			row["monitor-status"] = "skipped"
		default:
			result, e := monitor(name)
			if e != nil || len(result) != 1 {
				row["monitor-status"] = "unavailable"
				if stopOnError {
					stopped = true
				}
			} else {
				row["monitor-status"] = "ok"
				for _, key := range strings.Split(lteMonitorProperties, ",") {
					if v := safeLTEValue(result[0][key]); v != "" {
						row[key] = v
					}
				}
			}
		}
		section.Rows = append(section.Rows, row)
	}
	return section
}

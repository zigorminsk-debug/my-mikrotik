package mikrotik

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SnapshotSection has an explicit status: an empty successful result is not an error.
type SnapshotSection struct {
	Status    string              `json:"status"` // ok, error, skipped
	Rows      []map[string]string `json:"rows"`
	Truncated bool                `json:"truncated,omitempty"`
}
type NetworkSnapshot struct {
	Protocol   string                     `json:"protocol"`
	StartedAt  time.Time                  `json:"startedAt"`
	FinishedAt time.Time                  `json:"finishedAt"`
	Sections   map[string]SnapshotSection `json:"sections"`
}

type snapshotSpec struct {
	id, path string
	fields   []string
	accept   func(map[string]string) bool
}

var snapshotSpecs = []snapshotSpec{
	{"interfaces", "/interface", []string{"name", "type", "running", "disabled"}, nil},
	{"addresses", "/ip/address", []string{"address", "network", "interface", "actual-interface", "dynamic", "disabled", "invalid"}, nil},
	{"dhcp", "/ip/dhcp-client", []string{"interface", "status", "address", "gateway", "add-default-route", "default-route-distance", "disabled", "dynamic"}, nil},
	{"routes", "/ip/route", []string{"dst-address", "gateway", "immediate-gw", "routing-table", "routing-mark", "distance", "active", "dynamic", "disabled", "unreachable", "blackhole", "comment"}, func(m map[string]string) bool {
		// Missing destination is retained explicitly, not silently treated as non-default.
		return m["dst-address"] == "" || m["dst-address"] == "0.0.0.0/0" || m["comment"] == "mti-probe-link"
	}},
	{"scheduler", "/system/scheduler", []string{"name", "disabled", "interval", "run-count", "next-run"}, func(m map[string]string) bool { return m["name"] == "mti-failover" }},
	{"environment", "/system/script/environment", []string{"name", "value"}, func(m map[string]string) bool {
		switch m["name"] {
		case "mtiFailBackup", "mtiFailRouteGw", "mtiFailRouteDist", "mtiFailProbeGw", "mtiFailNoGw":
			return true
		}
		return false
	}},
	{"dhcpServers", "/ip/dhcp-server", []string{"name", "interface", "address-pool", "disabled", "invalid", "dynamic"}, nil},
	{"pools", "/ip/pool", []string{"name", "ranges", "next-pool"}, nil},
	{"dhcpNetworks", "/ip/dhcp-server/network", []string{"address", "gateway", "dns-server"}, nil},
}

const maxSnapshotRows = 500

type snapshotReader func(string) ([]map[string]string, error)

// buildSnapshot does not return arbitrary router records: fields are allowlisted.
func buildSnapshot(ctx context.Context, protocol string, read snapshotReader, stopOnError bool, includeHistory ...bool) *NetworkSnapshot {
	s := &NetworkSnapshot{Protocol: protocol, StartedAt: time.Now().UTC(), Sections: make(map[string]SnapshotSection)}
	stopped := false
	for _, spec := range snapshotSpecs {
		section := SnapshotSection{Status: "skipped", Rows: []map[string]string{}}
		if !stopped && ctx.Err() == nil {
			recs, err := read(spec.path)
			if err != nil {
				section.Status = "error"
				if stopOnError {
					stopped = true
				}
			} else {
				section.Status = "ok"
				for _, rec := range recs {
					if spec.accept != nil && !spec.accept(rec) {
						continue
					}
					if len(section.Rows) >= maxSnapshotRows {
						section.Truncated = true
						break
					}
					row := make(map[string]string)
					for _, key := range spec.fields {
						if v, ok := rec[key]; ok {
							row[key] = v
						}
					}
					// Only expose whether our exact tag is present, not arbitrary comments.
					if comment, ok := rec["comment"]; ok {
						if spec.id == "addresses" {
							row["mti-owned"] = fmt.Sprint(comment == "mti-lan")
						}
						if spec.id == "dhcpNetworks" {
							row["mti-owned"] = fmt.Sprint(comment == "mti-net")
						}
					}
					section.Rows = append(section.Rows, row)
				}
			}
		}
		s.Sections[spec.id] = section
	}
	if len(includeHistory) > 0 && includeHistory[0] {
		section := SnapshotSection{Status: "skipped", Rows: []map[string]string{}}
		if !stopped && ctx.Err() == nil {
			recs, err := read("/log")
			if err != nil {
				section.Status = "error"
			} else {
				section = historySection(recs)
			}
		}
		s.Sections["failoverHistory"] = section
	}
	s.FinishedAt = time.Now().UTC()
	return s
}

// CollectNetworkSnapshot uses the previously selected transport, never fallback.
// Identity and resource are re-read in the same session before collecting sections;
// matching them is only a consistency check, not proof of the physical device.
type NetworkSnapshotOptions struct {
	IncludeHistory bool
	IncludeLTE     bool
}

func CollectNetworkSnapshot(ctx context.Context, p Params, expected *Info, includeHistory ...bool) (*NetworkSnapshot, error) {
	return CollectNetworkSnapshotWithOptions(ctx, p, expected, NetworkSnapshotOptions{IncludeHistory: len(includeHistory) > 0 && includeHistory[0]})
}

// CollectNetworkSnapshotWithOptions adds explicitly requested diagnostics; LTE
// monitor is a one-shot observational command, not a continuous poll.
func CollectNetworkSnapshotWithOptions(ctx context.Context, p Params, expected *Info, options NetworkSnapshotOptions) (*NetworkSnapshot, error) {
	if expected == nil {
		return nil, fmt.Errorf("сначала подключитесь к устройству")
	}
	if err := p.normalize(); err != nil {
		return nil, err
	}
	if p.Protocol != "api" && p.Protocol != "rest" {
		return nil, fmt.Errorf("снимок доступен только через API или REST; подключитесь по IP")
	}
	if expected.Protocol != p.Protocol {
		return nil, fmt.Errorf("протокол не совпадает с последним подключением")
	}
	var read snapshotReader
	var readLTE snapshotReader
	var monitorLTE func(string) ([]map[string]string, error)
	if p.Protocol == "api" {
		c, err := dialAPI(ctx, &p)
		if err != nil {
			return nil, fmt.Errorf("не удалось подключиться для снимка")
		}
		defer c.Close()
		if err := c.login(p.User, p.Password); err != nil {
			return nil, fmt.Errorf("не удалось авторизоваться для снимка")
		}
		read = func(path string) ([]map[string]string, error) {
			recs, _, err := c.exec(path + "/print")
			return recs, err
		}
		readLTE = func(_ string) ([]map[string]string, error) {
			recs, _, err := c.exec("/interface/lte/print", "=.proplist=name,running,disabled")
			return recs, err
		}
		monitorLTE = func(name string) ([]map[string]string, error) {
			recs, _, err := c.exec("/interface/lte/monitor", "=numbers="+name, "=once=", "=.proplist="+lteMonitorProperties)
			return recs, err
		}
	} else {
		rc, err := newRESTClient(&p)
		if err != nil {
			return nil, err
		}
		read = func(path string) ([]map[string]string, error) {
			recs, err := rc.get(ctx, "/rest"+path)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]string, 0, len(recs))
			for _, rec := range recs {
				m := make(map[string]string, len(rec))
				for k := range rec {
					m[k] = jstr(rec, k)
				}
				out = append(out, m)
			}
			return out, nil
		}
		readLTE = func(_ string) ([]map[string]string, error) {
			recs, err := rc.get(ctx, "/rest/interface/lte?.proplist=name,running,disabled")
			if err != nil {
				return nil, err
			}
			out := make([]map[string]string, 0, len(recs))
			for _, rec := range recs {
				m := map[string]string{}
				for k := range rec {
					m[k] = jstr(rec, k)
				}
				out = append(out, m)
			}
			return out, nil
		}
		monitorLTE = func(name string) ([]map[string]string, error) {
			data, err := rc.post(ctx, "/rest/interface/lte/monitor", map[string]any{"numbers": name, "once": "", ".proplist": lteMonitorProperties})
			if err != nil {
				return nil, err
			}
			var raw json.RawMessage = data
			var arr []map[string]any
			if err := json.Unmarshal(raw, &arr); err != nil {
				var obj map[string]any
				if err = json.Unmarshal(raw, &obj); err != nil || obj == nil {
					return nil, fmt.Errorf("invalid monitor reply")
				}
				arr = []map[string]any{obj}
			}
			out := make([]map[string]string, 0, len(arr))
			for _, rec := range arr {
				m := map[string]string{}
				for k := range rec {
					m[k] = jstr(rec, k)
				}
				out = append(out, m)
			}
			return out, nil
		}
	}
	identity, err := read("/system/identity")
	if err != nil || len(identity) == 0 {
		return nil, fmt.Errorf("не удалось повторно прочитать identity; снимок не создан")
	}
	name := identity[0]["name"]
	if name == "" {
		name = identity[0]["identity"]
	}
	resource, err := read("/system/resource")
	if err != nil || len(resource) == 0 || resource[0]["version"] == "" {
		return nil, fmt.Errorf("не удалось повторно прочитать данные устройства; снимок не создан")
	}
	r := resource[0]
	if (expected.Identity != "" && name != expected.Identity) || r["version"] != expected.RouterOS || (expected.Architecture != "" && r["architecture-name"] != expected.Architecture) || (expected.Diagnostics != nil && r["board-name"] != expected.Diagnostics.ResourceBoard) {
		return nil, fmt.Errorf("данные устройства изменились с момента подключения; подключитесь заново")
	}
	if strings.TrimSpace(expected.RouterOS) == "" {
		return nil, fmt.Errorf("нет версии устройства; подключитесь заново")
	}
	snapshot := buildSnapshot(ctx, p.Protocol, read, p.Protocol == "api", options.IncludeHistory)
	if options.IncludeLTE {
		stopped := ctx.Err() != nil
		if p.Protocol == "api" {
			for _, section := range snapshot.Sections {
				if section.Status != "ok" {
					stopped = true
					break
				}
			}
		}
		if !strings.HasPrefix(expected.RouterOS, "7.") {
			snapshot.Sections["lte"] = SnapshotSection{Status: "unsupported", Rows: []map[string]string{}}
		} else if stopped {
			snapshot.Sections["lte"] = SnapshotSection{Status: "skipped", Rows: []map[string]string{}}
		} else {
			snapshot.Sections["lte"] = collectLTE(ctx, readLTE, monitorLTE, p.Protocol == "api")
		}
		snapshot.FinishedAt = time.Now().UTC()
	}
	return snapshot, nil
}

package mikrotik

import (
	"context"
	"fmt"
	"strings"
)

// Iface — интерфейс роутера (из /interface/print).
type Iface struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Comment  string `json:"comment,omitempty"`
	MAC      string `json:"mac,omitempty"`
	Disabled bool   `json:"disabled"`
	Bridge   string `json:"bridge,omitempty"` // configured bridge membership, including disabled ports
}

// toBool разбирает булевы значения RouterOS ("true"/"false"/"yes"/"no"/JSON bool).
func toBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "yes" || x == "1"
	case jsonNumberLike:
		return x.String() == "1"
	default:
		return false
	}
}

// jsonNumberLike — минимальный интерфейс для json.Number без импорта сюда.
type jsonNumberLike interface {
	String() string
}

func ifaceFromAttrs(m map[string]string) Iface {
	return Iface{
		Name:     m["name"],
		Type:     m["type"],
		Comment:  m["comment"],
		MAC:      m["mac-address"],
		Disabled: m["disabled"] == "true" || m["disabled"] == "yes" || m["disabled"] == "1",
	}
}

// addBridgePorts annotates physical/logical members before the constructor
// chooses an L3 interface. A disabled bridge port is still treated as a
// member, since it may be enabled without regenerating the configuration.
func addBridgePorts(ifaces []Iface, ports []map[string]string) error {
	byName := make(map[string]string, len(ports))
	for _, port := range ports {
		name, bridge := port["interface"], port["bridge"]
		if name == "" || bridge == "" {
			return fmt.Errorf("неполные данные о портах bridge")
		}
		if previous := byName[name]; previous != "" && previous != bridge {
			return fmt.Errorf("интерфейс %s указан в нескольких bridge", name)
		}
		byName[name] = bridge
	}
	for i := range ifaces {
		ifaces[i].Bridge = byName[ifaces[i].Name]
	}
	return nil
}

func bridgeMemberError(ifaces []Iface, name, context string) string {
	for _, iface := range ifaces {
		if iface.Name == name && iface.Bridge != "" {
			return fmt.Sprintf("%s: интерфейс %s входит в bridge %s; выберите подходящий интерфейс уровня IP или измените состав bridge вручную", context, name, iface.Bridge)
		}
	}
	return ""
}

// CollectInterfaces возвращает список интерфейсов роутера.
func CollectInterfaces(ctx context.Context, p Params) ([]Iface, error) {
	if err := p.normalize(); err != nil {
		return nil, err
	}
	if p.Protocol == "mac" {
		return interfacesMAC(ctx, &p)
	}
	var protocols []string
	switch p.Protocol {
	case "api":
		protocols = []string{"api"}
	case "rest":
		protocols = []string{"rest"}
	default:
		protocols = []string{"api", "rest"}
	}

	var errs []string
	for _, proto := range protocols {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err().Error())
			break
		}
		var (
			list []Iface
			err  error
		)
		switch proto {
		case "api":
			list, err = interfacesAPI(ctx, &p)
		case "rest":
			list, err = interfacesREST(ctx, &p)
		}
		if err == nil {
			return list, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %s", proto, err))
	}
	return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
}

func interfacesAPI(ctx context.Context, p *Params) ([]Iface, error) {
	c, err := dialAPI(ctx, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	if err := c.login(p.User, p.Password); err != nil {
		return nil, err
	}
	recs, _, err := c.exec("/interface/print")
	if err != nil {
		return nil, fmt.Errorf("чтение /interface: %w", err)
	}
	out := make([]Iface, 0, len(recs))
	for _, m := range recs {
		iface := ifaceFromAttrs(m)
		if iface.Name == "" {
			continue
		}
		out = append(out, iface)
	}
	ports, _, err := c.exec("/interface/bridge/port/print")
	if err != nil {
		return nil, fmt.Errorf("чтение портов bridge: %w", err)
	}
	if err := addBridgePorts(out, ports); err != nil {
		return nil, err
	}
	return out, nil
}

func interfacesREST(ctx context.Context, p *Params) ([]Iface, error) {
	rc, err := newRESTClient(p)
	if err != nil {
		return nil, err
	}
	recs, err := rc.get(ctx, "/rest/interface")
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(recs))
	for _, m := range recs {
		iface := Iface{
			Name:     jstr(m, "name"),
			Type:     jstr(m, "type"),
			Comment:  jstr(m, "comment"),
			MAC:      jstr(m, "mac-address"),
			Disabled: toBool(m["disabled"]),
		}
		if iface.Name == "" {
			continue
		}
		out = append(out, iface)
	}
	ports, err := rc.get(ctx, "/rest/interface/bridge/port")
	if err != nil {
		return nil, fmt.Errorf("чтение портов bridge: %w", err)
	}
	bridgePorts := make([]map[string]string, 0, len(ports))
	for _, port := range ports {
		bridgePorts = append(bridgePorts, map[string]string{"interface": jstr(port, "interface"), "bridge": jstr(port, "bridge")})
	}
	if err := addBridgePorts(out, bridgePorts); err != nil {
		return nil, err
	}
	return out, nil
}

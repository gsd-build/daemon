package preview

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type Target struct {
	Host string
	Port int
}

func (t Target) Addr() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

func (t Target) Addrs() []string {
	seen := map[string]struct{}{}
	var hosts []string
	add := func(host string) {
		if _, ok := seen[host]; ok {
			return
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}

	add(t.Host)
	add("localhost")
	add("127.0.0.1")
	add("::1")

	addrs := make([]string, 0, len(hosts))
	for _, host := range hosts {
		addrs = append(addrs, net.JoinHostPort(host, strconv.Itoa(t.Port)))
	}
	return addrs
}

func NormalizeTarget(host string, port int) (Target, error) {
	if port < 1 || port > 65535 {
		return Target{}, fmt.Errorf("invalid target port")
	}
	normalized := strings.Trim(strings.ToLower(host), "[]")
	if normalized == "localhost" || normalized == "::1" {
		return Target{Host: normalized, Port: port}, nil
	}
	ip := net.ParseIP(normalized)
	if ip == nil || !ip.IsLoopback() {
		return Target{}, fmt.Errorf("preview target must be loopback")
	}
	return Target{Host: normalized, Port: port}, nil
}

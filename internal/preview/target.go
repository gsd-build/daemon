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

func NormalizeTarget(host string, port int) (Target, error) {
	if port < 1 || port > 65535 {
		return Target{}, fmt.Errorf("invalid target port")
	}
	normalized := strings.Trim(strings.ToLower(host), "[]")
	if normalized == "localhost" || normalized == "::1" {
		return Target{Host: "127.0.0.1", Port: port}, nil
	}
	ip := net.ParseIP(normalized)
	if ip == nil || !ip.IsLoopback() {
		return Target{}, fmt.Errorf("preview target must be loopback")
	}
	return Target{Host: "127.0.0.1", Port: port}, nil
}

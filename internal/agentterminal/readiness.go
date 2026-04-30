package agentterminal

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	readinessURLPattern  = regexp.MustCompile(`(?i)\bhttps?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(?::([0-9]{1,5}))?(?:/[^\s'"<>)]*)?`)
	readinessHostPattern = regexp.MustCompile(`(?i)\b(?:localhost|127\.0\.0\.1|0\.0\.0\.0):([0-9]{1,5})\b`)
)

type ReadinessDetector struct {
	pattern *regexp.Regexp
	timeout time.Duration
	state   Readiness
	ports   []Port
	urls    []string
	seen    map[string]bool
}

func NewReadinessDetector(req StartRequest, limits Limits) (*ReadinessDetector, error) {
	var pattern *regexp.Regexp
	var err error
	if strings.TrimSpace(req.ReadyPattern) != "" {
		pattern, err = regexp.Compile(req.ReadyPattern)
		if err != nil {
			return nil, err
		}
	}
	timeout := limits.DefaultReadyTimeout
	if req.ReadyTimeoutMs > 0 {
		timeout = time.Duration(req.ReadyTimeoutMs) * time.Millisecond
	}
	state := Readiness{State: ReadinessWaiting, TimeoutMs: int(timeout / time.Millisecond)}
	d := &ReadinessDetector{pattern: pattern, timeout: timeout, state: state, seen: make(map[string]bool)}
	if req.ReadyPort > 0 {
		p := Port{Host: "127.0.0.1", Port: req.ReadyPort, URL: fmt.Sprintf("http://127.0.0.1:%d/", req.ReadyPort)}
		d.addPort(p)
		d.markReady("port", p.URL)
	}
	if strings.TrimSpace(req.ReadyURL) != "" {
		for _, p := range extractPorts(req.ReadyURL) {
			d.addPort(p)
		}
		d.addURL(normalizeReadyURL(req.ReadyURL, 0))
		d.markReady("url", req.ReadyURL)
	}
	return d, nil
}

func (d *ReadinessDetector) Observe(output string) (Readiness, []Port, []string, bool) {
	before := d.snapshotKey()
	for _, p := range extractPorts(output) {
		d.addPort(p)
	}
	if d.pattern != nil {
		if match := d.pattern.FindString(output); match != "" {
			d.markReady("pattern", match)
		}
	}
	if d.state.State != ReadinessReady && heuristicReady(output) {
		d.markReady("heuristic", strings.TrimSpace(output))
	}
	return d.state, clonePorts(d.ports), append([]string(nil), d.urls...), before != d.snapshotKey()
}

func (d *ReadinessDetector) MarkExit(exitCode int) (Readiness, bool) {
	before := d.snapshotKey()
	if exitCode == 0 {
		d.markReady("process_exit", "")
	} else {
		d.state.State = ReadinessFailed
		d.state.Source = "process_exit"
	}
	return d.state, before != d.snapshotKey()
}

func (d *ReadinessDetector) addPort(p Port) {
	if p.Port < 1 || p.Port > 65535 {
		return
	}
	key := fmt.Sprintf("port:%d", p.Port)
	if d.seen[key] {
		return
	}
	d.seen[key] = true
	if p.Host == "" {
		p.Host = "127.0.0.1"
	}
	if p.URL == "" {
		p.URL = fmt.Sprintf("http://127.0.0.1:%d/", p.Port)
	}
	d.ports = append(d.ports, p)
	d.addURL(p.URL)
}

func (d *ReadinessDetector) addURL(rawURL string) {
	if rawURL == "" {
		return
	}
	key := "url:" + rawURL
	if d.seen[key] {
		return
	}
	d.seen[key] = true
	d.urls = append(d.urls, rawURL)
}

func (d *ReadinessDetector) markReady(source string, matched string) {
	if d.state.State == ReadinessReady {
		return
	}
	d.state.State = ReadinessReady
	d.state.Source = source
	d.state.MatchedText = strings.TrimSpace(matched)
	d.state.ReadyAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func (d *ReadinessDetector) snapshotKey() string {
	var b strings.Builder
	b.WriteString(d.state.State)
	b.WriteString("|")
	b.WriteString(d.state.Source)
	b.WriteString("|")
	b.WriteString(d.state.MatchedText)
	for _, p := range d.ports {
		b.WriteString(fmt.Sprintf("|p:%s:%d:%s", p.Host, p.Port, p.URL))
	}
	for _, u := range d.urls {
		b.WriteString("|u:")
		b.WriteString(u)
	}
	return b.String()
}

func extractPorts(text string) []Port {
	seen := make(map[int]bool)
	var out []Port
	add := func(port int, rawURL string) {
		if port < 1 || port > 65535 || seen[port] {
			return
		}
		seen[port] = true
		out = append(out, Port{Host: "127.0.0.1", Port: port, URL: normalizeReadyURL(rawURL, port)})
	}
	for _, match := range readinessURLPattern.FindAllStringSubmatch(text, -1) {
		rawURL := match[0]
		port := 0
		if match[1] != "" {
			port, _ = strconv.Atoi(match[1])
		} else if parsed, err := url.Parse(rawURL); err == nil {
			switch strings.ToLower(parsed.Scheme) {
			case "http":
				port = 80
			case "https":
				port = 443
			}
		}
		add(port, rawURL)
	}
	for _, match := range readinessHostPattern.FindAllStringSubmatch(text, -1) {
		port, _ := strconv.Atoi(match[1])
		add(port, "")
	}
	return out
}

func normalizeReadyURL(rawURL string, port int) string {
	if rawURL == "" {
		return fmt.Sprintf("http://127.0.0.1:%d/", port)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		if port > 0 {
			return fmt.Sprintf("http://127.0.0.1:%d/", port)
		}
		return rawURL
	}
	if port == 0 {
		if parsedPort, err := strconv.Atoi(parsed.Port()); err == nil {
			port = parsedPort
		}
	}
	if port > 0 {
		parsed.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String()
}

func heuristicReady(output string) bool {
	normalized := strings.ToLower(output)
	patterns := []string{"local:", "localhost:", "listening on", "ready in", "compiled", "server running"}
	for _, pattern := range patterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

func clonePorts(in []Port) []Port {
	out := make([]Port, len(in))
	copy(out, in)
	return out
}

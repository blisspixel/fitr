// Package listen identifies the process bound to a local measured endpoint.
//
// The owned-runtime check in autoruntime stays a strict loopback proof for a
// process fitr started. This package answers a different question for a daemon
// fitr did not start: which PID holds this port, on any local address, over
// both IPv4 and IPv6. A table that cannot be read is unobserved. A row owned
// by another process is a different fact from a table that has no row.
package listen

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	StatusObserved   = "observed"
	StatusUnobserved = "unobserved"
	StatusNone       = "none"
)

var (
	ErrUnobserved = errors.New("listener is unobserved")
	ErrNoListener = errors.New("no listener matched the measured endpoint")
	ErrAmbiguous  = errors.New("more than one process listens on the measured endpoint")
)

// Row is one listening socket. PID is zero when the table named the socket
// but not the process that owns it.
type Row struct {
	Family   string
	IP       net.IP
	Port     int
	PID      int
	Wildcard bool
}

// Endpoint is the address a client used, not the address the daemon bound.
type Endpoint struct {
	Host  string
	Port  int
	Local bool
}

// ParseEndpoint reports whether raw is a local HTTP endpoint fitr can bind to
// a process. A hostname other than localhost is not resolved: DNS would turn
// an unobserved remote host into a guess.
func ParseEndpoint(raw string) (Endpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Endpoint{}, errors.New("endpoint is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Endpoint{}, errors.New("endpoint is not an http URL")
	}
	host := parsed.Hostname()
	portText := parsed.Port()
	if portText == "" {
		return Endpoint{}, errors.New("endpoint has no port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return Endpoint{}, errors.New("endpoint port is invalid")
	}
	return Endpoint{Host: host, Port: port, Local: localHost(host)}, nil
}

func localHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0", "::":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		network, ok := addr.(*net.IPNet)
		if ok && network.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// Covers reports whether a listening row accepts a connection to host.
// A wildcard covers every local host. A specific address covers only itself
// and the localhost name when the address is loopback. An IPv6 wildcard also
// covers an IPv4 loopback client, which is the dual-stack bind daemons use
// when they serve other machines.
func Covers(row Row, host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if row.Port < 1 || row.IP == nil {
		return false
	}
	if row.Wildcard {
		if row.Family == "ipv6" {
			return true
		}
		return host != "::1"
	}
	ip := net.ParseIP(host)
	switch host {
	case "localhost":
		return row.IP.IsLoopback()
	case "0.0.0.0":
		return row.Family == "ipv4" && row.Wildcard
	case "::":
		return row.Family == "ipv6" && row.Wildcard
	}
	if ip == nil {
		return false
	}
	return row.IP.Equal(ip)
}

// SelectPID returns the only process whose row covers host on port. Several
// PIDs, or any covering row that does not name a process, stay unobserved
// rather than picking one. Two rows owned by the same PID are one process.
func SelectPID(rows []Row, host string, port int) (int, error) {
	if port < 1 || port > 65535 {
		return 0, ErrUnobserved
	}
	seen := map[int]struct{}{}
	covered := 0
	for _, row := range rows {
		if row.Port != port || !Covers(row, host) {
			continue
		}
		covered++
		if row.PID <= 0 {
			return 0, ErrUnobserved
		}
		seen[row.PID] = struct{}{}
	}
	switch {
	case covered == 0:
		return 0, ErrNoListener
	case len(seen) != 1:
		return 0, ErrAmbiguous
	}
	for pid := range seen {
		return pid, nil
	}
	return 0, ErrUnobserved
}

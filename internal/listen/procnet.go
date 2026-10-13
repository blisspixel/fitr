package listen

import (
	"bufio"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"
)

// ParseProcNet reads one /proc/net/tcp or /proc/net/tcp6 snapshot. Only
// TCP_LISTEN (state 0A) rows are returned. The inode is carried in Row by
// encoding it into a side channel via the returned inode slice, kept aligned
// with rows. PIDs are filled by the caller from /proc/<pid>/fd.
func ParseProcNet(text string, family string) ([]procRow, error) {
	if family != "ipv4" && family != "ipv6" {
		return nil, errors.New("unknown proc net family")
	}
	var rows []procRow
	sc := bufio.NewScanner(strings.NewReader(text))
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			if strings.HasPrefix(line, "sl") {
				continue
			}
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return nil, errors.New("truncated proc net row")
		}
		if !strings.EqualFold(fields[3], "0A") {
			continue
		}
		ip, port, err := parseProcAddress(fields[1], family)
		if err != nil {
			return nil, err
		}
		inode := fields[9]
		if _, err := strconv.ParseUint(inode, 10, 64); err != nil {
			return nil, errors.New("proc net inode is not numeric")
		}
		wildcard := ip.IsUnspecified()
		rows = append(rows, procRow{Row: Row{Family: family, IP: ip, Port: port, Wildcard: wildcard}, Inode: inode})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

type procRow struct {
	Row   Row
	Inode string
}

func parseProcAddress(field, family string) (net.IP, int, error) {
	addr, portText, ok := strings.Cut(field, ":")
	if !ok {
		return nil, 0, errors.New("proc net address has no port")
	}
	portValue, err := strconv.ParseUint(portText, 16, 16)
	if err != nil {
		return nil, 0, errors.New("proc net port is invalid")
	}
	raw, err := hex.DecodeString(addr)
	if err != nil {
		return nil, 0, errors.New("proc net address is invalid")
	}
	switch family {
	case "ipv4":
		if len(raw) != 4 {
			return nil, 0, errors.New("proc net ipv4 address has the wrong width")
		}
		// The kernel prints the address little-endian.
		return net.IPv4(raw[3], raw[2], raw[1], raw[0]), int(portValue), nil
	case "ipv6":
		if len(raw) != 16 {
			return nil, 0, errors.New("proc net ipv6 address has the wrong width")
		}
		ip := make(net.IP, 16)
		// Four little-endian 32-bit words.
		for word := range 4 {
			base := word * 4
			ip[base] = raw[base+3]
			ip[base+1] = raw[base+2]
			ip[base+2] = raw[base+1]
			ip[base+3] = raw[base]
		}
		return ip, int(portValue), nil
	default:
		return nil, 0, errors.New("unknown proc net family")
	}
}

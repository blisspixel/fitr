package listen

import (
	"encoding/binary"
	"errors"
	"net"
)

// Windows MIB_TCPROW_OWNER_PID is 24 bytes and MIB_TCP6ROW_OWNER_PID is 56.
// Both layouts are the TCP_TABLE_OWNER_PID_LISTENER rows GetExtendedTcpTable
// returns. The v4 and v6 rows are not the same struct with a wider address,
// so one parser cannot serve both by changing a constant.
//
// Checked against the SDK headers shipped with mingw-w64 12 (MIB_TCPROW_OWNER_PID
// and MIB_TCP6ROW_OWNER_PID). The port occupies the low 16 bits in network order.

const (
	tcp4RowBytes = 24
	tcp6RowBytes = 56
)

func ParseTCP4OwnerPID(data []byte) ([]Row, error) {
	return parseOwnerTable(data, "ipv4", tcp4RowBytes, func(row []byte) Row {
		ip := net.IPv4(row[4], row[5], row[6], row[7])
		return Row{
			Family: "ipv4", IP: ip, Port: int(binary.BigEndian.Uint16(row[8:10])),
			PID: int(binary.LittleEndian.Uint32(row[20:24])), Wildcard: ip.Equal(net.IPv4zero),
		}
	})
}

func ParseTCP6OwnerPID(data []byte) ([]Row, error) {
	return parseOwnerTable(data, "ipv6", tcp6RowBytes, func(row []byte) Row {
		ip := net.IP(append([]byte(nil), row[0:16]...))
		wildcard := true
		for _, b := range ip {
			if b != 0 {
				wildcard = false
				break
			}
		}
		return Row{
			Family: "ipv6", IP: ip, Port: int(binary.BigEndian.Uint16(row[20:22])),
			PID: int(binary.LittleEndian.Uint32(row[52:56])), Wildcard: wildcard,
		}
	})
}

func parseOwnerTable(data []byte, family string, rowBytes int, decode func([]byte) Row) ([]Row, error) {
	if len(data) < 4 {
		return nil, errors.New("invalid " + family + " listener table")
	}
	n := binary.LittleEndian.Uint32(data)
	if n > 1<<20 {
		return nil, errors.New("invalid " + family + " listener table")
	}
	need := 4 + int(n)*rowBytes
	if need < 4 || need > len(data) {
		return nil, errors.New("truncated " + family + " listener table")
	}
	rows := make([]Row, 0, n)
	for i := range n {
		start := 4 + int(i)*rowBytes
		rows = append(rows, decode(data[start:start+rowBytes]))
	}
	return rows, nil
}

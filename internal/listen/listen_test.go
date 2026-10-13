package listen

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
)

func TestParseEndpointAcceptsLocalHTTPOnly(t *testing.T) {
	ep, err := ParseEndpoint("http://127.0.0.1:11434/api")
	if err != nil || !ep.Local || ep.Host != "127.0.0.1" || ep.Port != 11434 {
		t.Fatalf("endpoint = %+v, %v", ep, err)
	}
	if _, err := ParseEndpoint("http://example.test:11434"); err != nil {
		t.Fatal(err)
	}
	remote, err := ParseEndpoint("http://example.test:11434")
	if err != nil || remote.Local {
		t.Fatalf("remote endpoint = %+v, %v", remote, err)
	}
	for _, raw := range []string{"", "127.0.0.1:11434", "http://127.0.0.1", "ftp://127.0.0.1:1"} {
		if _, err := ParseEndpoint(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestSelectPIDKeepsWildcardAndAmbiguousRowsUnclaimed(t *testing.T) {
	loop := Row{Family: "ipv4", IP: net.IPv4(127, 0, 0, 1), Port: 11434, PID: 10}
	wild4 := Row{Family: "ipv4", IP: net.IPv4zero, Port: 11434, PID: 11, Wildcard: true}
	wild6 := Row{Family: "ipv6", IP: net.IPv6unspecified, Port: 11434, PID: 12, Wildcard: true}
	lan := Row{Family: "ipv4", IP: net.IPv4(192, 0, 2, 10), Port: 11434, PID: 13}
	otherPort := Row{Family: "ipv4", IP: net.IPv4(127, 0, 0, 1), Port: 80, PID: 14}

	pid, err := SelectPID([]Row{loop, otherPort}, "127.0.0.1", 11434)
	if err != nil || pid != 10 {
		t.Fatalf("specific loopback = %d, %v", pid, err)
	}
	pid, err = SelectPID([]Row{wild4}, "127.0.0.1", 11434)
	if err != nil || pid != 11 {
		t.Fatalf("ipv4 wildcard = %d, %v", pid, err)
	}
	pid, err = SelectPID([]Row{wild6}, "127.0.0.1", 11434)
	if err != nil || pid != 12 {
		t.Fatalf("ipv6 wildcard = %d, %v", pid, err)
	}
	pid, err = SelectPID([]Row{loop, loop}, "localhost", 11434)
	if err != nil || pid != 10 {
		t.Fatalf("same pid twice = %d, %v", pid, err)
	}
	if _, err := SelectPID([]Row{lan}, "127.0.0.1", 11434); !errors.Is(err, ErrNoListener) {
		t.Fatalf("other address = %v", err)
	}
	if _, err := SelectPID([]Row{wild4, loop}, "127.0.0.1", 11434); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("two pids = %v", err)
	}
	unnamed := wild4
	unnamed.PID = 0
	if _, err := SelectPID([]Row{unnamed, loop}, "127.0.0.1", 11434); !errors.Is(err, ErrUnobserved) {
		t.Fatalf("zero pid beside a real pid = %v", err)
	}
	if _, err := SelectPID(nil, "127.0.0.1", 11434); !errors.Is(err, ErrNoListener) {
		t.Fatalf("no row = %v", err)
	}
	if _, err := SelectPID([]Row{wild4}, "::1", 11434); !errors.Is(err, ErrNoListener) {
		t.Fatalf("ipv4 wildcard covered ipv6 loopback: %v", err)
	}
}

func TestTCPOwnerTablesDecodeBothFamilies(t *testing.T) {
	v4 := make([]byte, 4+24)
	binary.LittleEndian.PutUint32(v4, 1)
	// 127.0.0.1 in memory order, port 11434, pid 42. This is the same layout
	// the owned-runtime check reads, including a wildcard address of all zeros.
	copy(v4[8:12], []byte{127, 0, 0, 1})
	binary.BigEndian.PutUint16(v4[12:14], 11434)
	binary.LittleEndian.PutUint32(v4[24:28], 42)
	rows, err := ParseTCP4OwnerPID(v4)
	if err != nil || len(rows) != 1 || rows[0].PID != 42 || rows[0].Port != 11434 || rows[0].Wildcard {
		t.Fatalf("ipv4 row = %+v, %v", rows, err)
	}
	if !rows[0].IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("ipv4 address = %v", rows[0].IP)
	}
	binary.LittleEndian.PutUint32(v4[8:12], 0)
	rows, err = ParseTCP4OwnerPID(v4)
	if err != nil || !rows[0].Wildcard {
		t.Fatalf("ipv4 wildcard = %+v, %v", rows, err)
	}

	v6 := make([]byte, 4+56)
	binary.LittleEndian.PutUint32(v6, 1)
	v6[4+15] = 1 // ::1
	binary.BigEndian.PutUint16(v6[4+20:4+22], 11434)
	binary.LittleEndian.PutUint32(v6[4+52:4+56], 77)
	rows, err = ParseTCP6OwnerPID(v6)
	if err != nil || len(rows) != 1 || rows[0].PID != 77 || rows[0].Port != 11434 || rows[0].Wildcard {
		t.Fatalf("ipv6 row = %+v, %v", rows, err)
	}
	if !rows[0].IP.Equal(net.ParseIP("::1")) {
		t.Fatalf("ipv6 address = %v", rows[0].IP)
	}
	for i := 4; i < 20; i++ {
		v6[i] = 0
	}
	rows, err = ParseTCP6OwnerPID(v6)
	if err != nil || !rows[0].Wildcard {
		t.Fatalf("ipv6 wildcard = %+v, %v", rows, err)
	}
	if _, err := ParseTCP4OwnerPID(v4[:10]); err == nil {
		t.Fatal("truncated ipv4 table accepted")
	}
	if _, err := ParseTCP6OwnerPID(v6[:40]); err == nil {
		t.Fatal("truncated ipv6 table accepted")
	}
}

func TestProcNetKeepsListenRowsAndInode(t *testing.T) {
	text := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:2CB6 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 194818 1 0000000000000000 100 0 0 10 0\n" +
		"   1: 0100007F:2CB6 00000000:0000 01 00000000:00000000 00:00000000 00000000  1000        0 1 1 0000000000000000 100 0 0 10 0\n"
	rows, err := ParseProcNet(text, "ipv4")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	if rows[0].Inode != "194818" || rows[0].Row.Port != 0x2CB6 || rows[0].Row.Wildcard {
		t.Fatalf("row = %+v", rows[0])
	}
	if !rows[0].Row.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("address = %v", rows[0].Row.IP)
	}
	wild := "   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 9 1 0000000000000000 100 0 0 10 0\n"
	rows, err = ParseProcNet(wild, "ipv4")
	if err != nil || len(rows) != 1 || !rows[0].Row.Wildcard || rows[0].Row.Port != 80 {
		t.Fatalf("wildcard = %+v, %v", rows, err)
	}
	v6 := "   0: 00000000000000000000000001000000:2CB6 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 55 1 0000000000000000 100 0 0 10 0\n"
	rows, err = ParseProcNet(v6, "ipv6")
	if err != nil || len(rows) != 1 || rows[0].Inode != "55" || !rows[0].Row.IP.Equal(net.ParseIP("::1")) {
		t.Fatalf("ipv6 = %+v, %v", rows, err)
	}
	if _, err := ParseProcNet("0: short\n", "ipv4"); err == nil {
		t.Fatal("truncated proc row accepted")
	}
}

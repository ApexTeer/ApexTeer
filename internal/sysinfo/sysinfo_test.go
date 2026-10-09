package sysinfo

import (
	"testing"
	"time"
)

func TestParseLoadAvg(t *testing.T) {
	if got := parseLoadAvg([]byte("0.12 0.08 0.05 1/234 5678\n")); got != "0.12 0.08 0.05" {
		t.Fatalf("load = %q", got)
	}
	if got := parseLoadAvg([]byte("garbage")); got != "" {
		t.Fatalf("bad load should be empty, got %q", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	data := "MemTotal: 2048000 kB\nMemFree: 200000 kB\nMemAvailable: 1024000 kB\nSwapTotal: 1048576 kB\nSwapFree: 524288 kB\n"
	total, avail, swapTotal, swapFree := parseMeminfo([]byte(data))
	if total != 2048000*1024 {
		t.Fatalf("total = %d", total)
	}
	if avail != 1024000*1024 {
		t.Fatalf("avail = %d", avail)
	}
	if swapTotal != 1048576*1024 {
		t.Fatalf("swap total = %d", swapTotal)
	}
	if swapFree != 524288*1024 {
		t.Fatalf("swap free = %d", swapFree)
	}
	// Kernels without MemAvailable fall back to MemFree, and hosts without swap
	// report zero.
	_, avail, swapTotal, _ = parseMeminfo([]byte("MemTotal: 1024 kB\nMemFree: 512 kB\n"))
	if avail != 512*1024 {
		t.Fatalf("fallback avail = %d", avail)
	}
	if swapTotal != 0 {
		t.Fatalf("missing swap should be 0, got %d", swapTotal)
	}
}

func TestParseUptime(t *testing.T) {
	got := parseUptime([]byte("12345.67 98765.43\n"))
	want := time.Duration(12345.67 * float64(time.Second))
	if diff := got - want; diff < -time.Second || diff > time.Second {
		t.Fatalf("uptime = %s, want ~%s", got, want)
	}
	if got := parseUptime(nil); got != 0 {
		t.Fatalf("empty uptime = %s, want 0", got)
	}
}

func TestParseNetDev(t *testing.T) {
	data := "Inter-|   Receive                                                |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"    lo: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0\n" +
		"  eth0: 5000 50 0 0 0 0 0 0 7000 70 0 0 0 0 0 0\n" +
		"  eth1: 300 3 0 0 0 0 0 0 400 4 0 0 0 0 0 0\n"
	rx, tx := parseNetDev([]byte(data))
	if rx != 5300 || tx != 7400 {
		t.Fatalf("rx/tx = %d/%d, want 5300/7400 (loopback excluded)", rx, tx)
	}
	if rx, tx := parseNetDev([]byte("garbage\n")); rx != 0 || tx != 0 {
		t.Fatalf("unparseable input should sum to zero, got %d/%d", rx, tx)
	}
}

func TestParseDiskStats(t *testing.T) {
	// One whole disk, one NVMe disk, and the noise that must be ignored: a
	// partition of each (already inside its disk's counters), a device-mapper
	// and an md device (which would double-count the disks underneath them),
	// and the loop devices.
	data := "   8       0 sda 100 0 2000 10 50 0 4000 20 0 0 0\n" +
		"   8       1 sda1 90 0 1800 9 40 0 3600 18 0 0 0\n" +
		" 259       0 nvme0n1 10 0 300 1 5 0 600 2 0 0 0\n" +
		" 259       1 nvme0n1p1 8 0 240 0 4 0 480 1 0 0 0\n" +
		" 253       0 dm-0 7 0 140 0 3 0 280 0 0 0 0\n" +
		"   9       0 md0 1 0 20 0 1 0 40 0 0 0 0\n" +
		"   7       0 loop0 999 0 999000 0 999 0 999000 0 0 0 0\n" +
		"garbage that is not a diskstats line\n"
	read, write := parseDiskStats([]byte(data))
	wantRead := uint64((2000 + 300) * sectorSize)
	wantWrite := uint64((4000 + 600) * sectorSize)
	if read != wantRead {
		t.Fatalf("read = %d, want %d (whole disks only)", read, wantRead)
	}
	if write != wantWrite {
		t.Fatalf("write = %d, want %d (whole disks only)", write, wantWrite)
	}
	if read, write := parseDiskStats(nil); read != 0 || write != 0 {
		t.Fatalf("empty input should be zero, got %d/%d", read, write)
	}
}

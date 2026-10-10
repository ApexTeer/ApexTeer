//go:build linux

package sysinfo

import (
	"os"
	"strconv"
	"strings"
)

// CPUJiffies returns the processor's busy and total jiffies since boot, summed
// across every core, from the aggregate "cpu" line of /proc/stat. Both are
// counters, so a usage percentage is the difference between two reads: the
// caller keeps the previous pair. A missing file reports zero, which the caller
// reads as "not reported" rather than as an idle machine.
func CPUJiffies() (busy, total uint64) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	return parseCPUStat(data)
}

// parseCPUStat sums the first "cpu " line of /proc/stat. The fields are user,
// nice, system, idle, iowait, irq, softirq, steal (plus two guest fields that
// are already counted inside user and nice, so including them would double
// count). Busy is everything except idle and iowait.
func parseCPUStat(data []byte) (busy, total uint64) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var idle, iowait uint64
		for i, field := range fields {
			v, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				continue
			}
			total += v
			switch i {
			case 3:
				idle = v
			case 4:
				iowait = v
			}
		}
		return total - idle - iowait, total
	}
	return 0, 0
}

//go:build linux

package agent

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// memInfo returns total/used memory and total swap in KB.
func memInfo() (totalKB, usedKB, swapTotalKB int64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	vals := map[string]int64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		vals[key] = v
	}
	total := vals["MemTotal"]
	avail := vals["MemAvailable"]
	if avail == 0 {
		avail = vals["MemFree"]
	}
	return total, total - avail, vals["SwapTotal"]
}

// diskInfo returns total/used disk space (KB) of the filesystem holding path.
func diskInfo(path string) (totalKB, usedKB int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	b := int64(st.Bsize)
	total := int64(st.Blocks) * b / 1024
	free := int64(st.Bavail) * b / 1024
	return total, total - free
}

//go:build !windows

package main

import (
	"os"
	"strconv"
	"strings"
)

// readInterfaceTotals 返回网卡累计收发的字节数（开发模式用，读 Linux 的 /proc/net/dev，不算回环）。
func readInterfaceTotals() (received, sent uint64, ok bool) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, counters, found := strings.Cut(line, ":")
		fields := strings.Fields(counters)
		if !found || strings.TrimSpace(name) == "lo" || len(fields) < 9 {
			continue
		}
		in, inErr := strconv.ParseUint(fields[0], 10, 64)
		out, outErr := strconv.ParseUint(fields[8], 10, 64)
		if inErr == nil && outErr == nil {
			received, sent, ok = received+in, sent+out, true
		}
	}
	return received, sent, ok
}

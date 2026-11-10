package orchestrator

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type resourceReader struct {
	hostTotal, hostIdle, cpu, read, written uint64
	hostKnown, cpuKnown, ioKnown            bool
	at                                      time.Time
}

func readFields(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Fields(string(data))
}

func uintField(s string) (uint64, bool) {
	value, err := strconv.ParseUint(s, 10, 64)
	return value, err == nil
}

func (r *resourceReader) sample(dir string, now time.Time) resourceSample {
	result := resourceSample{at: now}
	stat, err := os.ReadFile("/proc/stat")
	line, _, _ := strings.Cut(string(stat), "\n")
	fields := strings.Fields(line)
	if err == nil && len(fields) >= 9 && fields[0] == "cpu" {
		var counters [8]uint64
		valid := true
		for i := range counters {
			value, ok := uintField(fields[i+1])
			counters[i] = value
			valid = valid && ok
		}
		if valid {
			var total uint64
			for _, value := range counters {
				total += value
			}
			idle := counters[3] + counters[4]
			if r.hostKnown && total > r.hostTotal && idle >= r.hostIdle && idle-r.hostIdle <= total-r.hostTotal {
				result.hostCPU = measured(100 * (1 - float64(idle-r.hostIdle)/float64(total-r.hostTotal)))
			}
			r.hostTotal, r.hostIdle, r.hostKnown = total, idle, true
		}
	}
	var total, available uint64
	var availableKnown bool
	mem, _ := os.ReadFile("/proc/meminfo")
	for _, line := range strings.Split(string(mem), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total, _ = uintField(fields[1])
		case "MemAvailable:":
			available, availableKnown = uintField(fields[1])
		}
	}
	if total > 0 {
		result.hostTotal = measured(float64(total) * 1024)
		if availableKnown && available <= total {
			result.hostRAM = measured(float64(total-available) * 1024)
		}
	}
	if fields := readFields("/proc/loadavg"); len(fields) >= 3 {
		if load, err := strconv.ParseFloat(fields[2], 64); err == nil {
			result.hostLoad = measured(load)
		}
	}
	var disk syscall.Statfs_t
	if syscall.Statfs(dir, &disk) == nil {
		result.diskFree = measured(float64(disk.Bavail) * float64(disk.Bsize))
		result.diskTotal = measured(float64(disk.Blocks) * float64(disk.Bsize))
	}
	if os.Getenv("ORCHESTRATOR_CONTAINER_METRICS") != "1" {
		return result
	}
	const cgroup = "/sys/fs/cgroup/"
	if fields := readFields(cgroup + "memory.current"); len(fields) == 1 {
		if ram, ok := uintField(fields[0]); ok {
			result.containerRAM = measured(float64(ram))
		}
	}
	if fields := readFields(cgroup + "memory.max"); len(fields) == 1 {
		if limit, ok := uintField(fields[0]); ok {
			result.containerLimit = measured(float64(limit))
		}
	}
	fields = readFields(cgroup + "cpu.stat")
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] != "usage_usec" {
			continue
		}
		if cpu, ok := uintField(fields[i+1]); ok {
			if elapsed := now.Sub(r.at); r.cpuKnown && elapsed > 0 && cpu >= r.cpu {
				result.containerCPU = measured(100 * float64(cpu-r.cpu) / (elapsed.Seconds() * 1e6))
			}
			r.cpu, r.cpuKnown = cpu, true
		}
		break
	}
	fields = readFields(cgroup + "io.stat")
	if fields != nil {
		var read, written uint64
		for _, field := range fields {
			key, number, _ := strings.Cut(field, "=")
			switch key {
			case "rbytes":
				value, _ := uintField(number)
				read += value
			case "wbytes":
				value, _ := uintField(number)
				written += value
			}
		}
		if r.ioKnown && read >= r.read && written >= r.written {
			result.readBytes = measured(float64(read - r.read))
			result.writtenBytes = measured(float64(written - r.written))
		}
		r.read, r.written, r.ioKnown = read, written, true
	}
	r.at = now
	return result
}

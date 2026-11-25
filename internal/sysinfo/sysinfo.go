package sysinfo

import (
	"context"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

type HostInfo struct {
	Hostname string    `json:"hostname"`
	OS       string    `json:"os"`
	Kernel   string    `json:"kernel"`
	Arch     string    `json:"arch"`
	Uptime   uint64    `json:"uptime"`
	Time     time.Time `json:"time"`
}

type CPUInfo struct {
	Percent float64 `json:"percent"`
	Cores   int     `json:"cores"`
	Model   string  `json:"model"`
	Load1   float64 `json:"load1"`
	Load5   float64 `json:"load5"`
	Load15  float64 `json:"load15"`
}

type MemInfo struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	UsedPercent float64 `json:"used_percent"`
	SwapTotal   uint64  `json:"swap_total"`
	SwapUsed    uint64  `json:"swap_used"`
	SwapPercent float64 `json:"swap_percent"`
}

type DiskInfo struct {
	Mount       string  `json:"mount"`
	Device      string  `json:"device"`
	Fstype      string  `json:"fstype"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	UsedPercent float64 `json:"used_percent"`
}

type NetInfo struct {
	RxPerSec uint64 `json:"rx_per_sec"`
	TxPerSec uint64 `json:"tx_per_sec"`
	RxTotal  uint64 `json:"rx_total"`
	TxTotal  uint64 `json:"tx_total"`
}

type ProcInfo struct {
	PID  int     `json:"pid"`
	User string  `json:"user"`
	CPU  float64 `json:"cpu"`
	Mem  float64 `json:"mem"`
	Cmd  string  `json:"cmd"`
}

type Snapshot struct {
	Host   HostInfo   `json:"host"`
	CPU    CPUInfo    `json:"cpu"`
	Mem    MemInfo    `json:"mem"`
	Disks  []DiskInfo `json:"disks"`
	Net    NetInfo    `json:"net"`
	Top    []ProcInfo `json:"top"`
	TopMem []ProcInfo `json:"top_mem"`
}

type Sampler struct {
	mu   sync.RWMutex
	snap Snapshot

	stopCh chan struct{}
	wg     sync.WaitGroup

	prevRx, prevTx uint64
	prevNetAt      time.Time

	started bool
}

func New() *Sampler {
	return &Sampler{stopCh: make(chan struct{})}
}

func (s *Sampler) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	s.collect()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-t.C:
				s.collect()
			}
		}
	}()
}

func (s *Sampler) Stop() {
	select {
	case <-s.stopCh:
		return
	default:
		close(s.stopCh)
	}
	s.wg.Wait()
}

func (s *Sampler) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := s.snap
	snap.Disks = append([]DiskInfo(nil), s.snap.Disks...)
	snap.Top = append([]ProcInfo(nil), s.snap.Top...)
	snap.TopMem = append([]ProcInfo(nil), s.snap.TopMem...)
	return snap
}

func (s *Sampler) collect() {
	var snap Snapshot

	if hi, err := host.Info(); err == nil {
		snap.Host = HostInfo{
			Hostname: hi.Hostname,
			OS:       strings.TrimSpace(hi.Platform + " " + hi.PlatformVersion),
			Kernel:   hi.KernelVersion,
			Arch:     hi.KernelArch,
			Uptime:   hi.Uptime,
			Time:     time.Now(),
		}
	} else {
		snap.Host.Time = time.Now()
	}

	snap.CPU.Cores = runtime.NumCPU()
	if pcts, err := cpu.Percent(0, false); err == nil && len(pcts) > 0 {
		snap.CPU.Percent = pcts[0]
	}
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		snap.CPU.Model = infos[0].ModelName
	}
	if avg, err := load.Avg(); err == nil {
		snap.CPU.Load1, snap.CPU.Load5, snap.CPU.Load15 = avg.Load1, avg.Load5, avg.Load15
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		snap.Mem.Total = vm.Total
		snap.Mem.Used = vm.Used
		snap.Mem.UsedPercent = vm.UsedPercent
	}
	if sw, err := mem.SwapMemory(); err == nil {
		snap.Mem.SwapTotal = sw.Total
		snap.Mem.SwapUsed = sw.Used
		snap.Mem.SwapPercent = sw.UsedPercent
	}

	snap.Disks = collectDisks()

	snap.Net = s.collectNet()
	snap.Top, snap.TopMem = collectTop()

	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
}

func collectDisks() []DiskInfo {
	var out []DiskInfo
	seen := map[string]bool{}
	parts, err := disk.Partitions(false)
	if err == nil {
		for _, p := range parts {
			switch p.Fstype {
			case "squashfs", "tmpfs", "devtmpfs", "overlay", "proc", "sysfs", "cgroup", "cgroup2":
				continue
			}
			if seen[p.Mountpoint] {
				continue
			}
			u, err := disk.Usage(p.Mountpoint)
			if err != nil || u.Total == 0 {
				continue
			}
			seen[p.Mountpoint] = true
			out = append(out, DiskInfo{
				Mount:       p.Mountpoint,
				Device:      p.Device,
				Fstype:      p.Fstype,
				Total:       u.Total,
				Used:        u.Used,
				UsedPercent: u.UsedPercent,
			})
		}
	}
	if len(out) == 0 {
		if u, err := disk.Usage("/"); err == nil {
			out = append(out, DiskInfo{Mount: "/", Total: u.Total, Used: u.Used, UsedPercent: u.UsedPercent})
		}
	}
	return out
}

func (s *Sampler) collectNet() NetInfo {
	var info NetInfo
	counters, err := net.IOCounters(false)
	if err != nil || len(counters) == 0 {
		return info
	}
	rx, tx := counters[0].BytesRecv, counters[0].BytesSent
	now := time.Now()
	info.RxTotal, info.TxTotal = rx, tx
	if !s.prevNetAt.IsZero() {
		elapsed := now.Sub(s.prevNetAt).Seconds()
		if elapsed > 0 {
			if rx >= s.prevRx {
				info.RxPerSec = uint64(float64(rx-s.prevRx) / elapsed)
			}
			if tx >= s.prevTx {
				info.TxPerSec = uint64(float64(tx-s.prevTx) / elapsed)
			}
		}
	}
	s.prevRx, s.prevTx, s.prevNetAt = rx, tx, now
	return info
}

// collectTop 返回按 CPU 和按内存分别排序的 Top 8 进程。
func collectTop() ([]ProcInfo, []ProcInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,user=,pcpu=,pmem=,args=").Output()
	if err != nil {
		return nil, nil
	}
	var procs []ProcInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		pcpu, _ := strconv.ParseFloat(fields[2], 64)
		pmem, _ := strconv.ParseFloat(fields[3], 64)
		cmd := strings.Join(fields[4:], " ")
		if len(cmd) > 90 {
			cmd = cmd[:90] + "…"
		}
		procs = append(procs, ProcInfo{PID: pid, User: fields[1], CPU: pcpu, Mem: pmem, Cmd: cmd})
	}
	return topBy(procs, func(p ProcInfo) float64 { return p.CPU }),
		topBy(procs, func(p ProcInfo) float64 { return p.Mem })
}

func topBy(procs []ProcInfo, key func(ProcInfo) float64) []ProcInfo {
	sorted := append([]ProcInfo(nil), procs...)
	sort.SliceStable(sorted, func(i, j int) bool { return key(sorted[i]) > key(sorted[j]) })
	if len(sorted) > 8 {
		sorted = sorted[:8]
	}
	return sorted
}

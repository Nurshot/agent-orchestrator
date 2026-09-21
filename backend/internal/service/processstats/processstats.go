// Package processstats reports the read-only OS-process footprint owned by AO.
package processstats

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// Process is one AO-owned process with only non-sensitive attribution fields.
type Process struct {
	PID       int32  `json:"pid"`
	RSSBytes  uint64 `json:"rssBytes"`
	Group     string `json:"group"`
	SessionID string `json:"sessionId,omitempty"`
}

// Snapshot is a point-in-time view of AO's resident process footprint.
type Snapshot struct {
	Processes     []Process `json:"processes"`
	TotalRSSBytes uint64    `json:"totalRssBytes"`
}

// Reader collects AO-owned processes from the host operating system.
type Reader interface {
	Snapshot(context.Context) (Snapshot, error)
}

// OSReader implements Reader using portable operating-system process APIs.
type OSReader struct{}

type rawProcess struct {
	pid, ppid int32
	rss       uint64
	command   string
}

// New constructs a host process reader.
func New() *OSReader { return &OSReader{} }

// Snapshot reads and attributes the current AO process forest.
func (r *OSReader) Snapshot(ctx context.Context) (Snapshot, error) {
	all, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	raws := make([]rawProcess, 0, len(all))
	roots := map[int32]Process{}
	for _, p := range all {
		ppid, e1 := p.PpidWithContext(ctx)
		cmd, e2 := p.CmdlineWithContext(ctx)
		mem, e3 := p.MemoryInfoWithContext(ctx)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		raws = append(raws, rawProcess{p.Pid, ppid, mem.RSS, cmd})
		group, sid := classify(int(p.Pid), ppid, cmd)
		if group != "" {
			roots[p.Pid] = Process{PID: p.Pid, RSSBytes: mem.RSS, Group: group, SessionID: sid}
		}
	}
	return attribute(raws, roots), nil
}

func attribute(raws []rawProcess, roots map[int32]Process) Snapshot {
	byPID := map[int32]rawProcess{}
	children := map[int32][]int32{}
	for _, p := range raws {
		byPID[p.pid] = p
		children[p.ppid] = append(children[p.ppid], p.pid)
	}
	type rootEntry struct {
		pid     int32
		process Process
	}
	entries := make([]rootEntry, 0, len(roots))
	for pid, root := range roots {
		entries = append(entries, rootEntry{pid, root})
	}
	priority := func(group string) int {
		if group == "control-plane" {
			return 0
		}
		if group == "tmux" {
			return 1
		}
		return 2
	}
	sort.Slice(entries, func(i, j int) bool {
		pi, pj := priority(entries[i].process.Group), priority(entries[j].process.Group)
		if pi != pj {
			return pi < pj
		}
		return entries[i].pid < entries[j].pid
	})
	owned := map[int32]Process{}
	var addTree func(int32, Process)
	addTree = func(pid int32, root Process) {
		p, ok := byPID[pid]
		if !ok {
			return
		}
		root.PID = p.pid
		root.RSSBytes = p.rss
		owned[pid] = root
		for _, child := range children[pid] {
			addTree(child, root)
		}
	}
	for _, entry := range entries {
		addTree(entry.pid, entry.process)
	}
	out := Snapshot{Processes: make([]Process, 0, len(owned))}
	for _, p := range owned {
		out.Processes = append(out.Processes, p)
		out.TotalRSSBytes += p.RSSBytes
	}
	sort.Slice(out.Processes, func(i, j int) bool {
		if out.Processes[i].Group != out.Processes[j].Group {
			return out.Processes[i].Group < out.Processes[j].Group
		}
		return out.Processes[i].PID < out.Processes[j].PID
	})
	return out
}

func classify(pid int, ppid int32, command string) (string, string) {
	if pid == os.Getpid() {
		return "control-plane", ""
	}
	fields := strings.Fields(command)
	if len(fields) >= 3 && isExecutable(fields[0], "ao") {
		if fields[1] == "pty-host" {
			if ppid == 1 {
				return "orphans", fields[2]
			}
			return "sessions", fields[2]
		}
		if len(fields) >= 6 && fields[1] == "agent-process" && fields[2] == "supervise" && fields[3] == "--session" {
			if ppid == 1 {
				return "orphans", fields[4]
			}
			return "sessions", fields[4]
		}
	}
	if len(fields) >= 3 && isExecutable(fields[0], "tmux") && fields[1] == "-L" && strings.HasPrefix(fields[2], "ao-") {
		return "tmux", ""
	}
	return "", ""
}

func isExecutable(value, name string) bool {
	base := strings.TrimSuffix(filepath.Base(value), ".exe")
	return strings.EqualFold(base, name)
}

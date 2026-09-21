package processstats

import "testing"

func TestClassifyManagedProcessRoots(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		ppid                    int32
		command, group, session string
	}{
		{"pty session", 42, "/usr/bin/ao pty-host worker-7 /tmp zsh", "sessions", "worker-7"},
		{"orphan pty", 1, "/usr/bin/ao pty-host old-2 /tmp zsh", "orphans", "old-2"},
		{"supervisor", 42, "ao agent-process supervise --session worker-9 --launch l-1 -- claude", "sessions", "worker-9"},
		{"private tmux", 1, "/usr/bin/tmux -L ao-private new-session", "tmux", ""},
		{"pty token in agent args", 42, "claude --prompt pty-host worker-7", "", ""},
		{"supervisor tokens in shell", 42, "sh -c ao agent-process supervise --session worker-9", "", ""},
		{"tmux tokens in shell", 1, "sh -c tmux -L ao-private", "", ""},
		{"non-AO socket", 1, "/usr/bin/tmux -L personal new-session", "", ""},
		{"unrelated", 1, "/usr/bin/node server.js", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group, session := classify(-1, tc.ppid, tc.command)
			if group != tc.group || session != tc.session {
				t.Fatalf("classify = %q, %q; want %q, %q", group, session, tc.group, tc.session)
			}
		})
	}
}

func TestAttributePrefersNestedSessionRootAndCountsEachProcessOnce(t *testing.T) {
	raws := []rawProcess{{pid: 10, ppid: 1, rss: 10}, {pid: 20, ppid: 10, rss: 20}, {pid: 30, ppid: 20, rss: 30}, {pid: 40, ppid: 30, rss: 40}}
	roots := map[int32]Process{10: {Group: "control-plane"}, 20: {Group: "tmux"}, 30: {Group: "sessions", SessionID: "worker-7"}}
	got := attribute(raws, roots)
	if len(got.Processes) != 4 || got.TotalRSSBytes != 100 {
		t.Fatalf("snapshot = %+v, want four unique processes totaling 100", got)
	}
	groups := map[int32]string{}
	for _, process := range got.Processes {
		groups[process.PID] = process.Group
	}
	if groups[10] != "control-plane" || groups[20] != "tmux" || groups[30] != "sessions" || groups[40] != "sessions" {
		t.Fatalf("groups = %#v", groups)
	}
}

func TestAttributeSessionRootOverridesDaemonTree(t *testing.T) {
	raws := []rawProcess{{pid: 10, ppid: 1, rss: 1}, {pid: 30, ppid: 10, rss: 2}, {pid: 40, ppid: 30, rss: 3}}
	got := attribute(raws, map[int32]Process{10: {Group: "control-plane"}, 30: {Group: "sessions", SessionID: "worker-9"}})
	if got.TotalRSSBytes != 6 {
		t.Fatalf("total = %d, want 6", got.TotalRSSBytes)
	}
	for _, process := range got.Processes {
		if process.PID >= 30 && (process.Group != "sessions" || process.SessionID != "worker-9") {
			t.Fatalf("process = %+v, want worker-9 session", process)
		}
	}
}

package handler

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestInstallerRejectsVersionInputBeforeSSH(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "test")
	h := &NodeOpsHandler{Nodes: env.nodes, Config: &ConfigHandler{}}
	for _, version := range []string{"1.14.4;touch /tmp/bad", "../bad", "1.15.0-alpha.13"} {
		rec := performJSONRequest(t, http.HandlerFunc(h.install), http.MethodPost, "/api/nodes/"+strconv.Itoa(node.ID)+"/install", map[string]any{"version": version})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("version=%q status=%d", version, rec.Code)
		}
	}
}

// Execute the generated installer with isolated downloads/service commands.
// These are negative tests of the gates: wrong version, missing stats support,
// incompatible config and a supervisor still running the old inode must all
// leave the previous executable in place.
func TestVerifiedInstallerPreservesWorkingBinaryOnFailure(t *testing.T) {
	for _, mode := range []string{"ok", "wrong-version", "missing-stats", "invalid-config", "restart-fails", "stale-process"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			stub := filepath.Join(dir, "commands")
			archiveDir := filepath.Join(dir, "archive")
			for _, d := range []string{stub, archiveDir} {
				if err := os.MkdirAll(d, 0755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
			}
			candidate := "#!/bin/sh\nif [ \"$1\" = version ]; then\n echo 'sing-box version 1.14.4'\n echo 'Tags: with_v2ray_api,with_quic'\nelse\n [ \"$MODE\" != invalid-config ]\nfi\n"
			if mode == "wrong-version" {
				candidate = "#!/bin/sh\necho 'sing-box version 1.13.18'\n"
			}
			if mode == "missing-stats" {
				candidate = "#!/bin/sh\necho 'sing-box version 1.14.4'\n"
			}
			write(filepath.Join(archiveDir, "sing-box"), candidate)
			archive := filepath.Join(dir, "core.tar.gz")
			if out, err := exec.Command("tar", "czf", archive, "-C", archiveDir, "sing-box").CombinedOutput(); err != nil {
				t.Fatalf("tar: %v %s", err, out)
			}
			bin := filepath.Join(dir, "absent-parent", "sing-box")
			if err := os.MkdirAll(filepath.Dir(bin), 0755); err != nil {
				t.Fatal(err)
			}
			old := "#!/bin/sh\necho old\n"
			write(bin, old)
			config := filepath.Join(dir, "config.json")
			write(config, "{}")
			write(filepath.Join(stub, "curl"), "#!/bin/sh\nfor last; do :; done\ncp \"$TEST_ARCHIVE\" \"$last\"\n")
			write(filepath.Join(stub, "sleep"), "#!/bin/sh\nexit 0\n")
			write(filepath.Join(stub, "systemctl"), "#!/bin/sh\ncase \"$1\" in\n show) echo 42;;\n restart) if [ \"$MODE\" = restart-fails ] && ! grep -q 'echo old' \"$TEST_BIN\"; then exit 1; fi;;\nesac\nexit 0\n")
			write(filepath.Join(stub, "readlink"), "#!/bin/sh\ncase \"$2\" in\n /proc/*) if [ \"$MODE\" = stale-process ]; then echo \"$TEST_BIN (deleted)\"; else echo \"$TEST_BIN\"; fi;;\n *) echo \"$2\";;\nesac\n")
			cmd := exec.Command("sh", "-c", verifiedInstallScript("https://example.invalid/core.tar.gz", bin, config, "1.14.4"))
			cmd.Env = append(os.Environ(), "PATH="+stub+":"+os.Getenv("PATH"), "MODE="+mode, "TEST_ARCHIVE="+archive, "TEST_BIN="+bin)
			out, err := cmd.CombinedOutput()
			actual, readErr := os.ReadFile(bin)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "ok" {
				if err != nil || string(actual) != candidate {
					t.Fatalf("valid install failed: %v %s", err, out)
				}
			} else if err == nil || string(actual) != old {
				t.Fatalf("failed gate changed binary: %v %s", err, out)
			}
		})
	}
}

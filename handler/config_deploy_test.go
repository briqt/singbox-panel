package handler

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicDeployOnlyRestartsWhenContentChanges(t *testing.T) {
	for _, same := range []bool{true, false} {
		name := "changed"
		if same {
			name = "identical"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			configPath, tempPath := filepath.Join(dir, "config.json"), filepath.Join(dir, "new.json")
			if err := os.WriteFile(configPath, []byte("old-config"), 0600); err != nil {
				t.Fatal(err)
			}
			content := "new-config"
			if same {
				content = "old-config"
			}
			if err := os.WriteFile(tempPath, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			fakeSystemctl := "#!/bin/sh\nprintf 'restart\\n' >> \"$RESTART_LOG\"\n"
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(fakeSystemctl), 0700); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(dir, "restart.log")
			command := exec.Command("sh", "-c", buildAtomicDeployCommand(configPath, tempPath))
			command.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "RESTART_LOG="+logPath)
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("部署脚本失败：%v %s", err, out)
			}
			actual, err := os.ReadFile(configPath)
			if err != nil || string(actual) != content {
				t.Fatalf("配置未生效：%v", err)
			}
			log, err := os.ReadFile(logPath)
			if same {
				if !os.IsNotExist(err) {
					t.Fatalf("相同配置不应重启：%s %v", log, err)
				}
			} else if err != nil || strings.TrimSpace(string(log)) != "restart" {
				t.Fatalf("变化配置必须重启且只重启一次：%s %v", log, err)
			}
			if info, err := os.Stat(configPath); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("配置必须仅所有者可读写：%v", err)
			}
		})
	}
}

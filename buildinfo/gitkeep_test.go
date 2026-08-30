package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// web/dist/.gitkeep is tracked so a fresh clone can `go build` before the
// frontend exists. But `pnpm build` wipes web/dist, which deletes it — leaving
// the tree dirty and stamping every CI release binary as "-dirty". The Makefile
// restores it; this test keeps the file tracked, since the restore is pointless
// if the file stops being part of the repo.
func TestGitkeepStaysTracked(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	out, err := exec.Command("git", "-C", root, "ls-files", "web/dist/.gitkeep").Output()
	if err != nil {
		t.Skipf("git ls-files failed: %v", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Fatal("web/dist/.gitkeep is no longer tracked; a clean clone cannot `go build` and release binaries will stamp themselves dirty")
	}
}

// The restore must happen inside the $(shell) that computes VERSION, not in a
// recipe line. Make expands the whole recipe — including $(shell git describe
// --dirty) — before running any of its commands, so a `touch` on the first
// line is already too late. v0.5.0 and v0.5.1 both shipped stamped "-dirty"
// with the restore sitting in the recipe, looking correct and doing nothing.
func TestVersionRestoresGitkeepBeforeDescribing(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Skip("Makefile unreadable")
	}
	for _, line := range strings.Split(string(makefile), "\n") {
		if !strings.HasPrefix(line, "VERSION") || !strings.Contains(line, "git describe") {
			continue
		}
		touched := strings.Index(line, "web/dist/.gitkeep")
		described := strings.Index(line, "git describe")
		if touched < 0 {
			t.Fatal("VERSION does not restore web/dist/.gitkeep; git describe will see a deleted tracked file and stamp the build dirty")
		}
		if touched > described {
			t.Fatal("VERSION restores .gitkeep after git describe; the restore must precede it in the same shell")
		}
		return
	}
	t.Fatal("no VERSION line invoking git describe found in Makefile")
}

// The restore has to live in build-stamped, the target CI actually invokes.
// Putting it only in `web` hid the bug locally for exactly this reason.
func TestBuildStampedRestoresGitkeep(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Skip("Makefile unreadable")
	}
	target, _, found := strings.Cut(afterTarget(string(makefile), "build-stamped:"), "\n\n")
	if !found && target == "" {
		t.Fatal("build-stamped target not found in Makefile")
	}
	if !strings.Contains(target, "web/dist/.gitkeep") {
		t.Fatal("build-stamped does not restore web/dist/.gitkeep; CI will build from a dirty tree")
	}
}

func afterTarget(makefile, target string) string {
	_, rest, found := strings.Cut(makefile, target)
	if !found {
		return ""
	}
	return rest
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

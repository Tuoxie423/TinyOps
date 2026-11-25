package files

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tuoxie423/tinyops/internal/config"
)

func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return New([]config.Project{{Name: "app", Unit: "app.service", Dir: dir}}), dir
}

func TestSaveCreatesFileAndDirs(t *testing.T) {
	s, dir := newTestService(t)

	saved, backup, n, err := s.Save("app", "conf/app.yaml", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if n != 5 {
		t.Fatalf("size = %d, want 5", n)
	}
	if backup != "" {
		t.Fatalf("backup = %q, want empty for new file", backup)
	}
	data, err := os.ReadFile(filepath.Join(dir, "conf", "app.yaml"))
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q", data)
	}
	if saved != filepath.Join(dir, "conf", "app.yaml") {
		t.Fatalf("saved path = %q", saved)
	}

	entries, _ := os.ReadDir(filepath.Join(dir, "conf"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pm-upload-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSaveBacksUpAndPreservesMode(t *testing.T) {
	s, dir := newTestService(t)
	target := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(target, []byte("old"), 0o750); err != nil {
		t.Fatal(err)
	}

	_, backup, _, err := s.Save("app", "run.sh", strings.NewReader("new"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if backup != target+".bak" {
		t.Fatalf("backup = %q, want %q", backup, target+".bak")
	}
	old, err := os.ReadFile(backup)
	if err != nil || string(old) != "old" {
		t.Fatalf("backup content = %q, err = %v", old, err)
	}
	cur, _ := os.ReadFile(target)
	if string(cur) != "new" {
		t.Fatalf("content = %q", cur)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o750 {
			t.Fatalf("mode = %v, want 0750", fi.Mode().Perm())
		}
	}
}

func TestSaveRejectsTraversal(t *testing.T) {
	s, _ := newTestService(t)
	cases := []string{
		"../evil.txt",
		"a/../../evil.txt",
		`..\evil.txt`,
		"/etc/passwd",
	}
	for _, c := range cases {
		_, _, _, err := s.Save("app", c, strings.NewReader("x"))
		if err == nil {
			t.Fatalf("Save(%q) 未拒绝越界路径", c)
		}
	}
}

func TestSaveRejectsDirectoryTarget(t *testing.T) {
	s, _ := newTestService(t)
	if _, _, _, err := s.Save("app", "sub/", strings.NewReader("x")); err == nil {
		t.Fatal("以 / 结尾的目录路径应当被拒绝")
	}
}

func TestList(t *testing.T) {
	s, dir := newTestService(t)
	if err := os.Mkdir(filepath.Join(dir, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List("app", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
	if !entries[0].IsDir || entries[0].Name != "conf" {
		t.Fatalf("目录应排在最前: %+v", entries[0])
	}
	if entries[1].Path != "a.txt" {
		t.Fatalf("path = %q", entries[1].Path)
	}
}

func TestUnknownProjectAndMissingDir(t *testing.T) {
	s := New([]config.Project{{Name: "nodir", Unit: "nodir.service"}})
	if _, err := s.List("missing", ""); err == nil {
		t.Fatal("未知项目应报错")
	}
	if _, err := s.List("nodir", ""); err == nil {
		t.Fatal("未配置目录的项目应报错")
	}
}

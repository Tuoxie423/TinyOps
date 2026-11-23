package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tuoxie423/tinyops/internal/config"
)

func TestAddPersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.added.json")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := config.Project{Name: "api", Unit: "api.service", Dir: "/opt/api", Desc: "主 API"}
	if err := s.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}

	s2, err := New(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	all := s2.All()
	if len(all) != 1 || all[0] != p {
		t.Fatalf("reloaded = %+v", all)
	}

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestAddRejectsDuplicates(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "p.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(config.Project{Name: "api", Unit: "api.service"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(config.Project{Name: "api", Unit: "other.service"}); err == nil {
		t.Fatal("重复名称应报错")
	}
	if err := s.Add(config.Project{Name: "api2", Unit: "api.service"}); err == nil {
		t.Fatal("重复 unit 应报错")
	}
	if got := len(s.All()); got != 1 {
		t.Fatalf("失败的添加不应留下记录, len=%d", got)
	}
}

func TestCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path); err == nil {
		t.Fatal("损坏的 JSON 应返回错误")
	}
}

func TestAddMultipleStaticProjects(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "p.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(config.Project{Name: "site-a", Type: "static", Site: "conf.d/a.conf"}); err != nil {
		t.Fatal(err)
	}
	// 静态项目 unit 为空，不能因为空 unit 被判重
	if err := s.Add(config.Project{Name: "site-b", Type: "static", Site: "conf.d/b.conf"}); err != nil {
		t.Fatalf("第二个静态项目不应失败: %v", err)
	}
	if got := len(s.All()); got != 2 {
		t.Fatalf("len = %d, want 2", got)
	}
}

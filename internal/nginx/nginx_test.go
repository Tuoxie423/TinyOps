package nginx

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
	root := t.TempDir()
	for _, d := range []string{"conf.d", "sites-enabled", "sites-available"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return New(config.NginxConfig{
		ConfigDir: root,
		Binary:    "nginx",
		Service:   "nginx.service",
	}, "systemctl"), root
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGroupsAutoDetect(t *testing.T) {
	s, root := newTestService(t)
	write(t, filepath.Join(root, "conf.d", "a.conf"), "server {}")
	write(t, filepath.Join(root, "sites-available", "site"), "server {}")
	write(t, filepath.Join(root, "nginx.conf"), "events {}")

	groups := s.Groups()
	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
	}
	want := []string{"conf.d", "sites-enabled", "sites-available", "all"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("groups = %v, want %v", ids, want)
	}
	for _, g := range groups {
		switch g.ID {
		case "conf.d":
			if g.Count != 1 {
				t.Fatalf("conf.d count = %d, want 1", g.Count)
			}
		case "all":
			if g.Count != 2 { // nginx.conf + conf.d/a.conf
				t.Fatalf("all count = %d, want 2", g.Count)
			}
		}
	}
}

func TestListShowsAllFilesInGroup(t *testing.T) {
	s, root := newTestService(t)
	write(t, filepath.Join(root, "conf.d", "a.conf"), "server {}")
	write(t, filepath.Join(root, "conf.d", "disabled.bak"), "old")
	write(t, filepath.Join(root, "conf.d", "sub", "b.conf"), "server {}")

	files, err := s.List("conf.d")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(files), files)
	}

	all, err := s.List("all")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	for _, f := range all {
		if f.Name == "disabled.bak" {
			t.Fatal("all 分组不应包含非配置文件")
		}
	}
}

func TestReadRejectsTraversal(t *testing.T) {
	s, _ := newTestService(t)
	cases := []struct{ group, path string }{
		{"conf.d", "../nginx.conf"},
		{"conf.d", "../../etc/passwd"},
		{"conf.d", "/etc/passwd"},
		{"nope", "a.conf"},
	}
	for _, c := range cases {
		if _, err := s.Read(c.group, c.path); err == nil {
			t.Fatalf("Read(%q, %q) 未报错", c.group, c.path)
		}
	}
}

func TestReadAndSaveWithBackup(t *testing.T) {
	s, root := newTestService(t)
	path := filepath.Join(root, "conf.d", "site.conf")
	write(t, path, "old content")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}

	res, err := s.Read("conf.d", "site.conf")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Content != "old content" || res.Path != "site.conf" || !res.Writable {
		t.Fatalf("unexpected result: %+v", res)
	}

	saved, backup, err := s.Save("conf.d", "site.conf", "new content")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved != "conf.d/site.conf" {
		t.Fatalf("saved = %q", saved)
	}
	if backup != "conf.d/site.conf.bak" {
		t.Fatalf("backup = %q", backup)
	}
	old, _ := os.ReadFile(path + ".bak")
	if string(old) != "old content" {
		t.Fatalf("backup content = %q", old)
	}
	cur, _ := os.ReadFile(path)
	if string(cur) != "new content" {
		t.Fatalf("content = %q", cur)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o640 {
			t.Fatalf("mode = %v, want 0640", fi.Mode().Perm())
		}
	}
}

func TestSaveRespectsSymlinkedSites(t *testing.T) {
	s, root := newTestService(t)
	target := filepath.Join(root, "sites-available", "demo.conf")
	link := filepath.Join(root, "sites-enabled", "demo.conf")
	write(t, target, "old")
	if err := os.Symlink(filepath.Join("..", "sites-available", "demo.conf"), link); err != nil {
		t.Skipf("当前环境无法创建软链: %v", err)
	}

	res, err := s.Read("sites-enabled", "demo.conf")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !res.Symlink || res.Target != "sites-available/demo.conf" {
		t.Fatalf("symlink info = %+v", res)
	}

	if _, backup, err := s.Save("sites-enabled", "demo.conf", "new"); err != nil {
		t.Fatalf("Save: %v", err)
	} else if backup != "sites-available/demo.conf.bak" {
		t.Fatalf("backup = %q", backup)
	}

	// 软链必须保留，写入的是 target
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("保存后软链被替换成了普通文件")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "new" {
		t.Fatalf("target content = %q", data)
	}
}

func TestSaveRejectsSymlinkOutsideRoot(t *testing.T) {
	s, root := newTestService(t)
	outside := filepath.Join(t.TempDir(), "outside.conf")
	write(t, outside, "secret")
	link := filepath.Join(root, "sites-enabled", "evil.conf")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前环境无法创建软链: %v", err)
	}
	if _, _, err := s.Save("sites-enabled", "evil.conf", "hacked"); err == nil {
		t.Fatal("指向配置目录外的软链应被拒绝")
	}
}

func TestSaveMissingFile(t *testing.T) {
	s, _ := newTestService(t)
	if _, _, err := s.Save("conf.d", "missing.conf", "x"); err == nil {
		t.Fatal("不存在的文件应报错")
	}
}

func listByPath(t *testing.T, files []FileInfo, p string) FileInfo {
	t.Helper()
	for _, f := range files {
		if f.Path == p {
			return f
		}
	}
	t.Fatalf("文件 %s 不在列表中: %+v", p, files)
	return FileInfo{}
}

func TestEnableAndDisableSite(t *testing.T) {
	s, root := newTestService(t)
	write(t, filepath.Join(root, "sites-available", "demo.conf"), "server {}")

	link, err := s.EnableSite("demo.conf")
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows 无法创建软链: %v", err)
		}
		t.Fatalf("EnableSite: %v", err)
	}
	if link != "sites-enabled/demo.conf" {
		t.Fatalf("link = %q", link)
	}
	li, err := os.Lstat(filepath.Join(root, "sites-enabled", "demo.conf"))
	if err != nil || li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("应创建软链, lstat=%v err=%v", li, err)
	}
	real, _ := filepath.EvalSymlinks(filepath.Join(root, "sites-enabled", "demo.conf"))
	if real != filepath.Join(root, "sites-available", "demo.conf") {
		t.Fatalf("软链指向 %q", real)
	}

	avail, err := s.List("sites-available")
	if err != nil {
		t.Fatal(err)
	}
	f := listByPath(t, avail, "demo.conf")
	if !f.Enabled || !f.CanToggle {
		t.Fatalf("sites-available 应显示已启用: %+v", f)
	}

	enabled, err := s.List("sites-enabled")
	if err != nil {
		t.Fatal(err)
	}
	ef := listByPath(t, enabled, "demo.conf")
	if !ef.Enabled || !ef.CanToggle || !ef.Symlink {
		t.Fatalf("sites-enabled 状态错误: %+v", ef)
	}

	removed, err := s.DisableSite("sites-available", "demo.conf")
	if err != nil {
		t.Fatalf("DisableSite: %v", err)
	}
	if removed != "sites-enabled/demo.conf" {
		t.Fatalf("removed = %q", removed)
	}
	if _, err := os.Lstat(filepath.Join(root, "sites-enabled", "demo.conf")); !os.IsNotExist(err) {
		t.Fatal("停用后软链应被删除")
	}
	if _, err := os.Stat(filepath.Join(root, "sites-available", "demo.conf")); err != nil {
		t.Fatal("真实配置文件不能被删除")
	}

	if _, err := s.EnableSite("demo.conf"); err != nil {
		t.Fatalf("重新启用失败: %v", err)
	}
	if _, err := s.DisableSite("sites-enabled", "demo.conf"); err != nil {
		t.Fatalf("从 sites-enabled 停用失败: %v", err)
	}
}

func TestDisableNeverRemovesRegularFile(t *testing.T) {
	s, root := newTestService(t)
	write(t, filepath.Join(root, "sites-enabled", "manual.conf"), "server {}")

	if _, err := s.DisableSite("sites-enabled", "manual.conf"); err == nil {
		t.Fatal("普通文件不能当成软链删除")
	}
	if _, err := os.Stat(filepath.Join(root, "sites-enabled", "manual.conf")); err != nil {
		t.Fatal("普通文件应保持不变")
	}

	files, _ := s.List("sites-enabled")
	f := listByPath(t, files, "manual.conf")
	if f.CanToggle {
		t.Fatalf("普通文件不应显示启用/停用按钮: %+v", f)
	}
}

func TestEnableConflictsAndNested(t *testing.T) {
	s, root := newTestService(t)
	write(t, filepath.Join(root, "sites-available", "demo.conf"), "server {}")
	write(t, filepath.Join(root, "sites-enabled", "demo.conf"), "regular file")

	if _, err := s.EnableSite("demo.conf"); err == nil {
		t.Fatal("sites-enabled 已有同名普通文件时应拒绝")
	}

	write(t, filepath.Join(root, "sites-available", "sub", "nested.conf"), "server {}")
	if _, err := s.EnableSite("sub/nested.conf"); err == nil {
		t.Fatal("嵌套目录中的文件不应允许启用")
	}
}

func TestEnableRequiresBothDirs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(config.NginxConfig{ConfigDir: root, Binary: "nginx", Service: "nginx.service"}, "systemctl")
	if _, err := s.EnableSite("x.conf"); err == nil {
		t.Fatal("没有 sites-available/sites-enabled 时应报错")
	}
}

func TestStaticSiteLifecycleSitesLayout(t *testing.T) {
	s, root := newTestService(t)
	write(t, filepath.Join(root, "sites-available", "demo.conf"), "server { root /tmp/site; }")

	online, err := s.SiteEnabled("sites-available/demo.conf")
	if err != nil || online {
		t.Fatalf("初始应为未上线: online=%v err=%v", online, err)
	}

	if _, err := s.OnlineSite("sites-available/demo.conf"); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows 无法创建软链: %v", err)
		}
		t.Fatalf("OnlineSite: %v", err)
	}
	if online, err = s.SiteEnabled("sites-available/demo.conf"); err != nil || !online {
		t.Fatalf("上线后状态错误: online=%v err=%v", online, err)
	}

	if _, err := s.OfflineSite("sites-available/demo.conf"); err != nil {
		t.Fatalf("OfflineSite: %v", err)
	}
	if online, err = s.SiteEnabled("sites-available/demo.conf"); err != nil || online {
		t.Fatalf("下线后状态错误: online=%v err=%v", online, err)
	}
}

func TestStaticSiteLifecycleConfD(t *testing.T) {
	s, root := newTestService(t)
	conf := filepath.Join(root, "conf.d", "site.conf")
	write(t, conf, "server { root /tmp/site; }")

	online, err := s.SiteEnabled("conf.d/site.conf")
	if err != nil || !online {
		t.Fatalf("初始应为在线: online=%v err=%v", online, err)
	}

	offPath, err := s.OfflineSite("conf.d/site.conf")
	if err != nil {
		t.Fatalf("OfflineSite: %v", err)
	}
	if offPath != "conf.d/site.conf.off" {
		t.Fatalf("offPath = %q", offPath)
	}
	if _, err := os.Stat(conf); !os.IsNotExist(err) {
		t.Fatal("下线后原文件应被改名")
	}
	if _, err := os.Stat(conf + ".off"); err != nil {
		t.Fatal(".off 文件应存在")
	}
	if online, err = s.SiteEnabled("conf.d/site.conf"); err != nil || online {
		t.Fatalf("下线后状态错误: online=%v err=%v", online, err)
	}

	back, err := s.OnlineSite("conf.d/site.conf")
	if err != nil {
		t.Fatalf("OnlineSite: %v", err)
	}
	if back != "conf.d/site.conf" {
		t.Fatalf("back = %q", back)
	}
	if _, err := os.Stat(conf); err != nil {
		t.Fatal("上线后原文件应恢复")
	}
}

func TestMatchSiteForDir(t *testing.T) {
	s, root := newTestService(t)
	siteDir := t.TempDir()
	write(t, filepath.Join(root, "sites-available", "demo.conf"),
		"server {\n    root "+siteDir+";\n}\n")
	write(t, filepath.Join(root, "conf.d", "other.conf"),
		"server {\n    root /var/www/other;\n}\n")

	cands := s.MatchSiteForDir(siteDir)
	if len(cands) != 1 || cands[0] != "sites-available/demo.conf" {
		t.Fatalf("candidates = %v", cands)
	}

	// 引号和尾斜杠也应匹配
	quoted := filepath.Join(root, "conf.d", "quoted.conf")
	write(t, quoted, "server {\n    root \""+siteDir+"/\";\n}\n")
	cands = s.MatchSiteForDir(siteDir)
	if len(cands) != 2 {
		t.Fatalf("quoted candidates = %v", cands)
	}
}

func TestNormalizeSiteMapsEnabled(t *testing.T) {
	s, root := newTestService(t)
	write(t, filepath.Join(root, "sites-available", "demo.conf"), "server {}")
	link := filepath.Join(root, "sites-enabled", "demo.conf")
	if err := os.Symlink(filepath.Join("..", "sites-available", "demo.conf"), link); err != nil {
		t.Skipf("当前环境无法创建软链: %v", err)
	}

	got, err := s.NormalizeSite("sites-enabled/demo.conf")
	if err != nil {
		t.Fatalf("NormalizeSite: %v", err)
	}
	if got != "sites-available/demo.conf" {
		t.Fatalf("normalized = %q", got)
	}
	if _, err := s.NormalizeSite("sites-enabled/missing.conf"); err == nil {
		t.Fatal("不存在的站点应报错")
	}
}

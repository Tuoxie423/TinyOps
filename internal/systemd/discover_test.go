package systemd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tuoxie423/tinyops/internal/config"
)

func writeUnit(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverIn(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()

	writeUnit(t, dir, "app-api.service",
		"[Unit]\nDescription=主 API 服务\n\n[Service]\nWorkingDirectory="+workDir+"\nExecStart=/bin/true\n")
	writeUnit(t, dir, "helper.service", "[Service]\nExecStart=/bin/true\n")
	writeUnit(t, dir, "skip-me.service", "[Unit]\nDescription=不要出现\n")
	writeUnit(t, dir, "not-a-service.timer", "[Timer]\nOnCalendar=daily\n")
	writeUnit(t, dir, "template@.service", "[Service]\nExecStart=/bin/true\n")

	existing := []config.Project{{Name: "manual", Unit: "manual.service", Dir: "/opt/manual"}}
	out := discoverIn([]string{dir}, []string{"skip-me.service"}, existing)

	if len(out) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(out), out)
	}
	if out[0].Name != "manual" {
		t.Fatalf("手动配置应排在最前: %+v", out)
	}
	if out[1].Name != "app-api" || out[1].Unit != "app-api.service" {
		t.Fatalf("unexpected app-api: %+v", out[1])
	}
	if out[1].Desc != "主 API 服务" {
		t.Fatalf("desc = %q", out[1].Desc)
	}
	if out[1].Dir != filepath.Clean(workDir) {
		t.Fatalf("dir = %q, want %q", out[1].Dir, workDir)
	}
	if out[2].Name != "helper" || out[2].Desc != "" || out[2].Dir != "" {
		t.Fatalf("unexpected helper: %+v", out[2])
	}
}

func TestDiscoverExcludeGlob(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "app-one.service", "[Service]\nExecStart=/bin/true\n")
	writeUnit(t, dir, "app-two.service", "[Service]\nExecStart=/bin/true\n")
	writeUnit(t, dir, "web.service", "[Service]\nExecStart=/bin/true\n")

	out := discoverIn([]string{dir}, []string{"app-*"}, nil)
	if len(out) != 1 || out[0].Name != "web" {
		t.Fatalf("out = %+v", out)
	}
}

func TestDiscoverSkipsExistingUnit(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "api.service", "[Unit]\nDescription=discovered\n[Service]\nExecStart=/bin/true\n")

	existing := []config.Project{{Name: "my-api", Unit: "api.service", Dir: "/opt/api"}}
	out := discoverIn([]string{dir}, nil, existing)
	if len(out) != 1 {
		t.Fatalf("已手动配置的 unit 不应重复出现: %+v", out)
	}
	if out[0].Name != "my-api" {
		t.Fatalf("手动配置应保留: %+v", out[0])
	}
}

func TestParseUnitFileQuotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.service")
	writeUnit(t, dir, "x.service",
		"[Unit]\nDescription=\"带引号的描述\"\n[Service]\nWorkingDirectory=-'/tmp/some dir'\n")
	desc, wd := parseUnitFile(path)
	if desc != "带引号的描述" {
		t.Fatalf("desc = %q", desc)
	}
	if wd != "/tmp/some dir" {
		t.Fatalf("wd = %q", wd)
	}
}

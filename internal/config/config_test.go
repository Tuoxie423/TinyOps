package config

import "testing"

func TestMergePrecedence(t *testing.T) {
	base := []Project{{Name: "api", Unit: "api.service", Desc: "from config"}}
	added := []Project{
		{Name: "api", Unit: "api.service", Desc: "duplicate"},
		{Name: "web", Unit: "web.service"},
	}
	discovered := []Project{
		{Name: "worker", Unit: "worker.service"},
		{Name: "web2", Unit: "web.service"},
	}

	out := Merge(base, added, discovered)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(out), out)
	}
	if out[0].Desc != "from config" {
		t.Fatalf("先出现的应优先: %+v", out[0])
	}
	if out[1].Name != "web" || out[2].Name != "worker" {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestValidUnit(t *testing.T) {
	valid := []string{"app-api.service", "worker@1.service", "nginx.timer", "a.service"}
	for _, u := range valid {
		if !ValidUnit(u) {
			t.Fatalf("%q 应该合法", u)
		}
	}
	invalid := []string{"", "--help.service", "-x.service", "app", "../etc/passwd", "a b.service"}
	for _, u := range invalid {
		if ValidUnit(u) {
			t.Fatalf("%q 不应合法", u)
		}
	}
}

func TestMergeStaticProjectsWithEmptyUnit(t *testing.T) {
	a := []Project{{Name: "site-a", Type: "static", Site: "conf.d/a.conf"}}
	b := []Project{{Name: "site-b", Type: "static", Site: "conf.d/b.conf"}}
	out := Merge(a, b)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(out), out)
	}
}

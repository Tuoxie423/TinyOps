package systemd

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tuoxie423/tinyops/internal/config"
)

var DefaultUnitDirs = []string{
	"/etc/systemd/system",
	"/usr/local/lib/systemd/system",
}

// 指向系统目录的软链不算自建。
var vendorUnitDirs = []string{
	"/lib/systemd/system",
	"/usr/lib/systemd/system",
}

func Discover(existing []config.Project, exclude []string) []config.Project {
	return discoverIn(DefaultUnitDirs, exclude, existing)
}

func discoverIn(dirs, exclude []string, existing []config.Project) []config.Project {
	merged := append([]config.Project(nil), existing...)

	seenUnit := make(map[string]bool, len(existing))
	seenName := make(map[string]bool, len(existing))
	for _, p := range existing {
		seenUnit[p.Unit] = true
		seenName[p.Name] = true
	}

	var found []config.Project
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".service") {
				continue
			}
			unit := e.Name()
			// 跳过 foo@.service 模板
			if strings.Contains(unit, "@") {
				continue
			}
			if seenUnit[unit] || isExcluded(unit, exclude) {
				continue
			}

			unitPath := filepath.Join(dir, unit)
			info, err := e.Info()
			if err != nil {
				continue
			}
			realPath := unitPath
			if info.Mode()&os.ModeSymlink != 0 {
				resolved, err := filepath.EvalSymlinks(unitPath)
				if err != nil {
					continue
				}
				if isVendorUnit(resolved) {
					continue
				}
				realPath = resolved
			}
			st, err := os.Stat(realPath)
			if err != nil || !st.Mode().IsRegular() {
				continue
			}

			name := strings.TrimSuffix(unit, ".service")
			if seenName[name] {
				continue
			}

			desc, workDir := parseUnitFile(realPath)
			p := config.Project{Name: name, Desc: desc, Unit: unit}
			if workDir != "" && filepath.IsAbs(workDir) {
				if fi, err := os.Stat(workDir); err == nil && fi.IsDir() {
					p.Dir = filepath.Clean(workDir)
				}
			}

			seenUnit[unit] = true
			seenName[name] = true
			found = append(found, p)
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return append(merged, found...)
}

func isVendorUnit(p string) bool {
	clean := filepath.Clean(p)
	for _, dir := range vendorUnitDirs {
		if clean == dir || strings.HasPrefix(clean, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func isExcluded(unit string, patterns []string) bool {
	name := strings.TrimSuffix(unit, ".service")
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if pat == unit || pat == name {
			return true
		}
		if ok, _ := path.Match(pat, unit); ok {
			return true
		}
		if ok, _ := path.Match(pat, name); ok {
			return true
		}
	}
	return false
}

func parseUnitFile(p string) (desc, workDir string) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", ""
	}
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch {
		case section == "unit" && key == "Description" && desc == "":
			desc = unquote(val)
		case section == "service" && key == "WorkingDirectory" && workDir == "":
			workDir = parsePathValue(val)
		}
	}
	return desc, workDir
}

func parsePathValue(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimSpace(strings.TrimPrefix(v, "-"))
	if v == "" {
		return ""
	}
	if v[0] == '"' || v[0] == '\'' {
		q := v[0]
		if end := strings.IndexByte(v[1:], q); end >= 0 {
			return v[1 : 1+end]
		}
	}
	if fields := strings.Fields(v); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

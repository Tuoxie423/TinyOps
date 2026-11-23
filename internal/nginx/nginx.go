package nginx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tuoxie423/tinyops/internal/config"
	"github.com/tuoxie423/tinyops/internal/fileutil"
)

const maxFileSize = 2 << 20 // 2 MiB

type FileInfo struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	Symlink   bool      `json:"symlink"`
	Target    string    `json:"target,omitempty"`
	Enabled   bool      `json:"enabled,omitempty"`
	CanToggle bool      `json:"can_toggle,omitempty"`
}

type Group struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Count int    `json:"count"`
}

type ReadResult struct {
	Group    string `json:"group"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	Size     int64  `json:"size"`
	Symlink  bool   `json:"symlink"`
	Target   string `json:"target,omitempty"`
	Writable bool   `json:"writable"`
}

type group struct {
	id   string
	name string
	path string
}

type Service struct {
	root      string
	binary    string
	unit      string
	systemctl string

	groups []group
	byID   map[string]group
	roots  []string // 允许读写的真实目录（已解析软链）
}

func New(c config.NginxConfig, systemctl string) *Service {
	s := &Service{
		root:      filepath.Clean(c.ConfigDir),
		binary:    c.Binary,
		unit:      c.Service,
		systemctl: systemctl,
		byID:      make(map[string]group),
	}

	var dirs []group
	if len(c.Dirs) > 0 {
		for _, d := range c.Dirs {
			dirs = append(dirs, group{id: d.Name, name: d.Name, path: filepath.Clean(d.Path)})
		}
	} else {
		for _, name := range []string{"conf.d", "sites-enabled", "sites-available"} {
			p := filepath.Join(s.root, name)
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				dirs = append(dirs, group{id: name, name: name, path: p})
			}
		}
	}

	if fi, err := os.Stat(s.root); err == nil && fi.IsDir() {
		dirs = append(dirs, group{id: "all", name: "全部文件", path: s.root})
	}

	for _, g := range dirs {
		if _, dup := s.byID[g.id]; dup {
			continue
		}
		s.groups = append(s.groups, g)
		s.byID[g.id] = g
	}
	s.refreshRoots()
	return s
}

func (s *Service) Dir() string { return s.root }

func (s *Service) refreshRoots() {
	s.roots = s.roots[:0]
	for _, g := range s.groups {
		if real, err := filepath.EvalSymlinks(g.path); err == nil {
			s.roots = append(s.roots, real)
		}
	}
}

func (s *Service) allowed(real string) bool {
	for _, r := range s.roots {
		if within(r, real) {
			return true
		}
	}
	return false
}

func (s *Service) Groups() []Group {
	out := make([]Group, 0, len(s.groups))
	for _, g := range s.groups {
		count := 0
		if files, err := s.List(g.id); err == nil {
			count = len(files)
		}
		out = append(out, Group{ID: g.id, Name: g.name, Path: g.path, Count: count})
	}
	return out
}

func (s *Service) List(groupID string) ([]FileInfo, error) {
	g, ok := s.byID[groupID]
	if !ok {
		return nil, fmt.Errorf("未知的配置分组: %s", groupID)
	}
	files, err := s.listDir(g.path, groupID == "all")
	if err != nil {
		return nil, err
	}
	s.markSiteState(groupID, files)
	return files, nil
}

func (s *Service) markSiteState(groupID string, files []FileInfo) {
	enabled, hasEnabled := s.byID["sites-enabled"]
	available, hasAvailable := s.byID["sites-available"]
	if !hasEnabled {
		return
	}

	switch groupID {
	case "sites-enabled":
		for i := range files {
			if strings.Contains(files[i].Path, "/") || !files[i].Symlink {
				continue
			}
			files[i].CanToggle = true
			files[i].Enabled = true
		}
	case "sites-available":
		if !hasAvailable {
			return
		}
		for i := range files {
			if strings.Contains(files[i].Path, "/") {
				continue
			}
			files[i].CanToggle = true
			link := filepath.Join(enabled.path, files[i].Name)
			li, err := os.Lstat(link)
			if err != nil || li.Mode()&os.ModeSymlink == 0 {
				continue
			}
			linkReal, err := filepath.EvalSymlinks(link)
			if err != nil {
				continue
			}
			fileReal, err := filepath.EvalSymlinks(filepath.Join(available.path, files[i].Path))
			if err != nil {
				continue
			}
			if linkReal == fileReal {
				files[i].Enabled = true
			}
		}
	}
}

func (s *Service) siteDirs() (enabled, available string, ok bool) {
	en, hasEnabled := s.byID["sites-enabled"]
	av, hasAvailable := s.byID["sites-available"]
	if !hasEnabled || !hasAvailable {
		return "", "", false
	}
	return en.path, av.path, true
}

func (s *Service) EnableSite(rel string) (string, error) {
	enabledDir, _, ok := s.siteDirs()
	if !ok {
		return "", errors.New("未检测到 sites-enabled / sites-available 目录")
	}
	_, clean, _, real, err := s.resolve("sites-available", rel)
	if err != nil {
		return "", err
	}
	if filepath.Dir(clean) != "." {
		return "", errors.New("只能启用 sites-available 根目录下的文件")
	}

	name := filepath.Base(clean)
	linkPath := filepath.Join(enabledDir, name)
	if li, err := os.Lstat(linkPath); err == nil {
		if li.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("sites-enabled/%s 已存在且不是软链，拒绝覆盖", name)
		}
		cur, err := filepath.EvalSymlinks(linkPath)
		if err == nil && cur == real {
			return s.displayPath(linkPath), nil // 已经是启用状态
		}
		return "", fmt.Errorf("sites-enabled/%s 已指向其他文件，请先手动处理", name)
	}

	target, err := filepath.Rel(enabledDir, real)
	if err != nil {
		target = real
	}
	if err := os.Symlink(target, linkPath); err != nil {
		return "", err
	}
	return s.displayPath(linkPath), nil
}

func (s *Service) DisableSite(groupID, rel string) (string, error) {
	enabledDir, _, ok := s.siteDirs()
	if !ok {
		return "", errors.New("未检测到 sites-enabled / sites-available 目录")
	}

	var linkPath string
	switch groupID {
	case "sites-enabled":
		clean, err := cleanRel(rel)
		if err != nil {
			return "", err
		}
		if filepath.Dir(clean) != "." {
			return "", errors.New("只能操作 sites-enabled 根目录下的软链")
		}
		linkPath = filepath.Join(enabledDir, clean)
	case "sites-available":
		_, clean, _, real, err := s.resolve("sites-available", rel)
		if err != nil {
			return "", err
		}
		if filepath.Dir(clean) != "." {
			return "", errors.New("只能停用 sites-available 根目录下的站点")
		}
		linkPath = filepath.Join(enabledDir, filepath.Base(clean))
		li, err := os.Lstat(linkPath)
		if err != nil || li.Mode()&os.ModeSymlink == 0 {
			return "", errors.New("该站点当前未启用")
		}
		cur, err := filepath.EvalSymlinks(linkPath)
		if err != nil || cur != real {
			return "", errors.New("sites-enabled 中的软链未指向该文件，拒绝删除")
		}
	default:
		return "", errors.New("只能从 sites-enabled 或 sites-available 分组启用/停用站点")
	}

	li, err := os.Lstat(linkPath)
	if err != nil {
		return "", err
	}
	if li.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("目标不是软链，拒绝删除")
	}
	if err := os.Remove(linkPath); err != nil {
		return "", err
	}
	return s.displayPath(linkPath), nil
}

func splitSite(site string) (groupID, rel string, err error) {
	site = strings.TrimSpace(site)
	groupID, rel, ok := strings.Cut(site, "/")
	groupID = strings.TrimSpace(groupID)
	rel = strings.TrimSpace(rel)
	if !ok || groupID == "" || rel == "" {
		return "", "", errors.New("站点配置格式应为 分组/文件，例如 sites-available/demo.conf")
	}
	return groupID, rel, nil
}

func (s *Service) groupPath(groupID, rel string) (group, string, error) {
	g, ok := s.byID[groupID]
	if !ok {
		return group{}, "", fmt.Errorf("未知的配置分组: %s", groupID)
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return group{}, "", err
	}
	abs := filepath.Join(g.path, clean)
	if !within(g.path, abs) {
		return group{}, "", errors.New("路径越界：只能访问配置目录内的文件")
	}
	return g, abs, nil
}

func (s *Service) SiteEnabled(site string) (bool, error) {
	groupID, rel, err := splitSite(site)
	if err != nil {
		return false, err
	}

	switch groupID {
	case "sites-available":
		enabledDir, _, ok := s.siteDirs()
		if !ok {
			return false, errors.New("未检测到 sites-enabled / sites-available 目录")
		}
		_, abs, err := s.groupPath(groupID, rel)
		if err != nil {
			return false, err
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return false, fmt.Errorf("配置不存在: %s", site)
		}
		link := filepath.Join(enabledDir, filepath.Base(abs))
		li, err := os.Lstat(link)
		if err != nil || li.Mode()&os.ModeSymlink == 0 {
			return false, nil
		}
		linkReal, err := filepath.EvalSymlinks(link)
		if err != nil {
			return false, nil
		}
		return linkReal == real, nil

	case "sites-enabled":
		_, abs, err := s.groupPath(groupID, rel)
		if err != nil {
			return false, err
		}
		li, err := os.Lstat(abs)
		if err != nil {
			return false, err
		}
		return li.Mode()&os.ModeSymlink != 0, nil

	case "all":
		return false, errors.New("请使用具体的配置分组")

	default:
		_, abs, err := s.groupPath(groupID, rel)
		if err != nil {
			return false, err
		}
		if _, err := os.Stat(abs); err == nil {
			return true, nil
		}
		if _, err := os.Stat(abs + ".off"); err == nil {
			return false, nil
		}
		return false, fmt.Errorf("配置不存在: %s", site)
	}
}

// 软链布局删软链，其他分组改名 .off。
func (s *Service) OfflineSite(site string) (string, error) {
	groupID, rel, err := splitSite(site)
	if err != nil {
		return "", err
	}

	switch groupID {
	case "sites-available":
		return s.DisableSite("sites-available", rel)
	case "sites-enabled":
		return s.DisableSite("sites-enabled", rel)
	case "all":
		return "", errors.New("请使用具体的配置分组")
	default:
		_, abs, err := s.groupPath(groupID, rel)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("配置不存在: %s", site)
		}
		if _, err := os.Stat(abs + ".off"); err == nil {
			return "", errors.New("已存在同名 .off 文件，无法下线")
		}
		if err := os.Rename(abs, abs+".off"); err != nil {
			return "", err
		}
		return s.displayPath(abs + ".off"), nil
	}
}

func (s *Service) OnlineSite(site string) (string, error) {
	groupID, rel, err := splitSite(site)
	if err != nil {
		return "", err
	}

	switch groupID {
	case "sites-available":
		return s.EnableSite(rel)
	case "sites-enabled":
		if avail, ok := s.byID["sites-available"]; ok {
			clean, err := cleanRel(rel)
			if err != nil {
				return "", err
			}
			base := filepath.Base(clean)
			if _, err := os.Stat(filepath.Join(avail.path, base)); err == nil {
				return s.EnableSite(base)
			}
		}
		return "", errors.New("无法自动上线：sites-available 中没有对应文件")
	case "all":
		return "", errors.New("请使用具体的配置分组")
	default:
		_, abs, err := s.groupPath(groupID, rel)
		if err != nil {
			return "", err
		}
		off := abs + ".off"
		if _, err := os.Stat(off); err != nil {
			return "", errors.New("该站点当前未下线")
		}
		if _, err := os.Stat(abs); err == nil {
			return "", errors.New("原配置文件已存在，拒绝覆盖")
		}
		if err := os.Rename(off, abs); err != nil {
			return "", err
		}
		return s.displayPath(abs), nil
	}
}

func (s *Service) NormalizeSite(site string) (string, error) {
	groupID, rel, err := splitSite(site)
	if err != nil {
		return "", err
	}
	if groupID == "all" {
		return "", errors.New("请使用具体的配置分组")
	}
	_, abs, err := s.groupPath(groupID, rel)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		// 允许关联 .off 配置
		if _, offErr := os.Stat(abs + ".off"); offErr != nil {
			return "", fmt.Errorf("配置不存在: %s", site)
		}
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return "", err
	}
	if groupID == "sites-enabled" {
		if avail, ok := s.byID["sites-available"]; ok {
			base := filepath.Base(clean)
			if _, err := os.Stat(filepath.Join(avail.path, base)); err == nil {
				return "sites-available/" + base, nil
			}
		}
	}
	return groupID + "/" + filepath.ToSlash(clean), nil
}

var siteRootRe = regexp.MustCompile(`(?m)^\s*(?:root|alias)\s+([^;]+);`)

func (s *Service) MatchSiteForDir(dir string) []string {
	dir = filepath.Clean(dir)
	var out []string
	seen := make(map[string]bool)

	for _, g := range s.groups {
		if g.id == "all" || g.id == "sites-enabled" {
			continue
		}
		files, err := s.listDir(g.path, false)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Symlink ||
				strings.HasSuffix(f.Path, ".bak") ||
				strings.HasSuffix(f.Path, ".off") ||
				strings.HasSuffix(f.Path, ".tmp") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(g.path, filepath.FromSlash(f.Path)))
			if err != nil {
				continue
			}
			matched := false
			for _, m := range siteRootRe.FindAllStringSubmatch(string(data), -1) {
				v := strings.TrimSpace(m[1])
				v = strings.Trim(v, `"'`)
				v = strings.TrimSpace(v)
				if v == "" || strings.Contains(v, "$") || !filepath.IsAbs(v) {
					continue
				}
				if filepath.Clean(v) == dir {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			id := g.id + "/" + f.Path
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) listDir(dir string, configOnly bool) ([]FileInfo, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s 不是目录", dir)
	}

	var out []FileInfo
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil
		}
		if p == dir {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if strings.Count(rel, string(filepath.Separator)) >= 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if configOnly && !isConfigFile(d.Name()) {
			return nil
		}

		symlink := d.Type()&os.ModeSymlink != 0
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		target := ""
		if symlink {
			real, rerr := filepath.EvalSymlinks(p)
			if rerr != nil {
				return nil // 悬空软链，跳过
			}
			st, serr := os.Stat(p)
			if serr != nil || st.IsDir() {
				return nil
			}
			info = st
			target = s.displayPath(real)
		}
		out = append(out, FileInfo{
			Path:    filepath.ToSlash(rel),
			Name:    d.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Symlink: symlink,
			Target:  target,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func isConfigFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".conf" || ext == ".inc" || name == "nginx.conf" || name == "mime.types"
}

func cleanRel(rel string) (string, error) {
	raw := strings.TrimSpace(rel)
	if raw == "" {
		return "", errors.New("缺少 path 参数")
	}
	if filepath.IsAbs(raw) || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) {
		return "", errors.New("请使用相对配置目录的路径")
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("非法路径")
	}
	return clean, nil
}

func (s *Service) resolve(groupID, rel string) (g group, clean, abs, real string, err error) {
	g, ok := s.byID[groupID]
	if !ok {
		return g, "", "", "", fmt.Errorf("未知的配置分组: %s", groupID)
	}
	clean, err = cleanRel(rel)
	if err != nil {
		return g, "", "", "", err
	}
	abs = filepath.Join(g.path, clean)
	if !within(g.path, abs) {
		return g, "", "", "", errors.New("路径越界：只能访问配置目录内的文件")
	}
	real, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return g, "", "", "", err
	}
	if !s.allowed(real) {
		return g, "", "", "", errors.New("目标不在允许的 nginx 配置目录内")
	}
	return g, clean, abs, real, nil
}

func (s *Service) Read(groupID, rel string) (ReadResult, error) {
	g, clean, abs, real, err := s.resolve(groupID, rel)
	if err != nil {
		return ReadResult{}, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return ReadResult{}, err
	}
	if fi.IsDir() {
		return ReadResult{}, errors.New("目标是目录")
	}
	if fi.Size() > maxFileSize {
		return ReadResult{}, fmt.Errorf("文件过大（%d 字节，上限 2MB）", fi.Size())
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return ReadResult{}, err
	}
	res := ReadResult{
		Group:    g.id,
		Path:     filepath.ToSlash(clean),
		Content:  string(data),
		Size:     fi.Size(),
		Symlink:  abs != real,
		Writable: true,
	}
	if res.Symlink {
		res.Target = s.displayPath(real)
	}
	return res, nil
}

// 备份后原子写入；软链写真实目标。
func (s *Service) Save(groupID, rel, content string) (saved, backup string, err error) {
	if len(content) > maxFileSize {
		return "", "", fmt.Errorf("内容超过 %d 字节上限", maxFileSize)
	}
	_, _, _, real, err := s.resolve(groupID, rel)
	if err != nil {
		return "", "", err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", "", err
	}
	if fi.IsDir() {
		return "", "", errors.New("目标是目录")
	}
	mode := fi.Mode().Perm()
	uid, gid, hasOwner := fileutil.OwnedBy(fi)

	backupPath := real + ".bak"
	if berr := copyFile(real, backupPath); berr == nil {
		backup = s.displayPath(backupPath)
	}

	dir := filepath.Dir(real)
	tmp, err := os.CreateTemp(dir, ".pm-edit-*")
	if err != nil {
		return "", "", err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			os.Remove(tmpName)
		}
	}()

	if _, err = tmp.WriteString(content); err != nil {
		tmp.Close()
		return "", "", err
	}
	if err = tmp.Chmod(mode); err != nil {
		tmp.Close()
		return "", "", err
	}
	if hasOwner {
		fileutil.SetOwner(tmpName, uid, gid)
	}
	if err = tmp.Close(); err != nil {
		return "", "", err
	}
	if err = os.Rename(tmpName, real); err != nil {
		return "", "", err
	}
	done = true
	return s.displayPath(real), backup, nil
}

func (s *Service) displayPath(abs string) string {
	if rel, err := filepath.Rel(s.root, abs); err == nil && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(abs)
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	fi, err := in.Stat()
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if uid, gid, ok := fileutil.OwnedBy(fi); ok {
		fileutil.SetOwner(tmp, uid, gid)
	}
	return os.Rename(tmp, dst)
}

func (s *Service) Test(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, s.binary, "-t").CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("nginx -t 失败")
	}
	return string(out), nil
}

func (s *Service) Reload(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, s.systemctl, "reload", s.unit).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("重载 nginx 失败: %v", err)
	}
	return string(out), nil
}

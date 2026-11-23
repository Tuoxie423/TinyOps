package files

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tuoxie423/tinyops/internal/config"
	"github.com/tuoxie423/tinyops/internal/fileutil"
)

type Service struct {
	mu       sync.RWMutex
	projects map[string]config.Project
}

func New(projects []config.Project) *Service {
	m := make(map[string]config.Project, len(projects))
	for _, p := range projects {
		m[p.Name] = p
	}
	return &Service{projects: m}
}

func (s *Service) Project(name string) (config.Project, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[name]
	return p, ok
}

func (s *Service) Add(p config.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[p.Name]; ok {
		return fmt.Errorf("项目名称已存在: %s", p.Name)
	}
	for _, x := range s.projects {
		if p.Unit != "" && x.Unit == p.Unit {
			return fmt.Errorf("unit 已存在: %s", p.Unit)
		}
	}
	s.projects[p.Name] = p
	return nil
}

type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
}

func cleanRel(rel string) (string, error) {
	raw := strings.TrimSpace(rel)
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) || filepath.IsAbs(raw) {
		return "", errors.New("请使用相对项目目录的路径")
	}
	clean := path.Clean(strings.ReplaceAll(raw, "\\", "/"))
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("非法路径")
	}
	return clean, nil
}

func (s *Service) resolve(project, rel string) (p config.Project, clean, abs string, err error) {
	s.mu.RLock()
	p, ok := s.projects[project]
	s.mu.RUnlock()
	if !ok {
		return p, "", "", fmt.Errorf("项目不存在: %s", project)
	}
	if p.Dir == "" {
		return p, "", "", fmt.Errorf("项目 %s 未配置目录", project)
	}
	clean, err = cleanRel(rel)
	if err != nil {
		return p, "", "", err
	}
	abs = filepath.Join(p.Dir, filepath.FromSlash(clean))
	back, err := filepath.Rel(p.Dir, abs)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return p, "", "", errors.New("路径越界：只能操作项目目录内的文件")
	}
	return p, clean, abs, nil
}

func (s *Service) List(project, rel string) ([]Entry, error) {
	_, clean, abs, err := s.resolve(project, rel)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(items))
	for _, it := range items {
		info, ierr := it.Info()
		if ierr != nil {
			continue
		}
		out = append(out, Entry{
			Name:    it.Name(),
			Path:    path.Join(clean, it.Name()),
			IsDir:   it.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().Perm().String(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (s *Service) Abs(project, rel string) (string, error) {
	_, _, abs, err := s.resolve(project, rel)
	return abs, err
}

// 临时文件 + rename，避免覆盖运行中二进制时 ETXTBSY。
func (s *Service) Save(project, rel string, src io.Reader) (saved, backup string, size int64, err error) {
	_, _, abs, err := s.resolve(project, rel)
	if err != nil {
		return "", "", 0, err
	}
	if strings.HasSuffix(rel, "/") || rel == "" {
		return "", "", 0, errors.New("目标必须是文件")
	}
	if fi, statErr := os.Stat(abs); statErr == nil && fi.IsDir() {
		return "", "", 0, errors.New("目标是目录")
	}

	dir := filepath.Dir(abs)
	if err := fileutil.MkdirAllOwned(dir, 0o755); err != nil {
		return "", "", 0, err
	}

	mode := os.FileMode(0o644)
	exists := false
	uid, gid := -1, -1
	if fi, statErr := os.Stat(abs); statErr == nil {
		exists = true
		mode = fi.Mode().Perm()
		if u, g, ok := fileutil.OwnedBy(fi); ok {
			uid, gid = u, g
		}
	} else if u, g, ok := fileutil.Ownership(dir); ok {
		// 新文件继承父目录属主。
		uid, gid = u, g
	}

	tmp, err := os.CreateTemp(dir, ".pm-upload-*")
	if err != nil {
		return "", "", 0, err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()

	size, err = io.Copy(tmp, src)
	if err != nil {
		tmp.Close()
		return "", "", 0, err
	}
	if err = tmp.Chmod(mode); err != nil {
		tmp.Close()
		return "", "", 0, err
	}
	fileutil.SetOwner(tmpName, uid, gid)
	if err = tmp.Close(); err != nil {
		return "", "", 0, err
	}

	if exists {
		backup = abs + ".bak"
		if berr := copyFile(abs, backup); berr != nil {
			backup = ""
		}
	}
	if err = os.Rename(tmpName, abs); err != nil {
		return "", "", 0, err
	}
	return abs, backup, size, nil
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

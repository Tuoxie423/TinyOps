package systemd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tuoxie423/tinyops/internal/config"
	"github.com/tuoxie423/tinyops/internal/fileutil"
)

const maxUnitSize = 1 << 20 // 1 MiB

type Manager struct {
	mu         sync.RWMutex
	projects   []config.Project
	byName     map[string]config.Project
	Systemctl  string
	Journalctl string
}

func New(projects []config.Project, systemctl string) *Manager {
	m := &Manager{
		projects:  projects,
		byName:    make(map[string]config.Project, len(projects)),
		Systemctl: systemctl,
	}
	for _, p := range projects {
		m.byName[p.Name] = p
	}
	if p, err := exec.LookPath("journalctl"); err == nil {
		m.Journalctl = p
	} else {
		m.Journalctl = "/usr/bin/journalctl"
	}
	return m
}

func (m *Manager) Projects() []config.Project {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]config.Project(nil), m.projects...)
}

func (m *Manager) Find(name string) (config.Project, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.byName[name]
	return p, ok
}

func (m *Manager) Add(p config.Project) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if p.Type == "static" {
		if p.Unit != "" {
			return fmt.Errorf("静态项目不能配置 unit")
		}
	} else if !config.ValidUnit(p.Unit) {
		return fmt.Errorf("非法的 unit 名: %q", p.Unit)
	}
	if _, ok := m.byName[p.Name]; ok {
		return fmt.Errorf("项目名称已存在: %s", p.Name)
	}
	for _, x := range m.projects {
		if p.Unit != "" && x.Unit == p.Unit {
			return fmt.Errorf("unit 已存在: %s", p.Unit)
		}
	}
	m.projects = append(m.projects, p)
	m.byName[p.Name] = p
	return nil
}

type UnitInfo struct {
	LoadState    string
	FragmentPath string
	Description  string
}

func (m *Manager) Inspect(ctx context.Context, unit string) (UnitInfo, error) {
	var info UnitInfo
	if !config.ValidUnit(unit) {
		return info, fmt.Errorf("非法的 unit 名: %q", unit)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, m.Systemctl, "show", unit,
		"--no-pager",
		"--property=LoadState,FragmentPath,Description",
	).Output()
	if err != nil {
		return info, fmt.Errorf("执行 systemctl 失败: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "LoadState":
			info.LoadState = v
		case "FragmentPath":
			info.FragmentPath = v
		case "Description":
			info.Description = v
		}
	}
	return info, nil
}

type Status struct {
	Name        string `json:"name"`
	Desc        string `json:"desc"`
	Unit        string `json:"unit"`
	Dir         string `json:"dir"`
	Type        string `json:"type,omitempty"`
	Site        string `json:"site,omitempty"`
	LoadState   string `json:"load_state"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
	Active      bool   `json:"active"`
	State       string `json:"state"`
	Enabled     string `json:"enabled"`
	MainPID     string `json:"main_pid"`
	Since       string `json:"since"`
	Err         string `json:"err,omitempty"`
}

func (m *Manager) Status(ctx context.Context, p config.Project) Status {
	st := Status{Name: p.Name, Desc: p.Desc, Unit: p.Unit, Dir: p.Dir, Type: p.Type, Site: p.Site}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, m.Systemctl, "show", p.Unit,
		"--no-pager",
		"--property=LoadState,ActiveState,SubState,UnitFileState,MainPID,ActiveEnterTimestamp",
	).Output()
	if err != nil {
		st.Err = err.Error()
		return st
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "LoadState":
			st.LoadState = v
		case "ActiveState":
			st.ActiveState = v
			st.Active = v == "active"
		case "SubState":
			st.SubState = v
		case "UnitFileState":
			st.Enabled = v
		case "MainPID":
			st.MainPID = v
		case "ActiveEnterTimestamp":
			st.Since = v
		}
	}
	st.State = st.ActiveState
	if st.SubState != "" {
		st.State += " (" + st.SubState + ")"
	}
	return st
}

func (m *Manager) Action(ctx context.Context, unit, action string) (string, error) {
	switch action {
	case "start", "stop", "restart", "reload":
	default:
		return "", fmt.Errorf("不支持的操作: %s", action)
	}
	if !config.ValidUnit(unit) {
		return "", fmt.Errorf("非法的 unit 名: %q", unit)
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, m.Systemctl, action, unit).CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return string(out), errors.New("操作超时（60 秒）")
		}
		if strings.Contains(string(out), "Interactive authentication required") ||
			strings.Contains(string(out), "Access denied") {
			return string(out), errors.New("权限不足：需要让 TinyOps 以 root 运行，或为对应 unit 配置 sudo 免密")
		}
		return string(out), fmt.Errorf("systemctl %s %s 失败: %v", action, unit, err)
	}
	return string(out), nil
}

func (m *Manager) Logs(ctx context.Context, unit string, lines int) (string, error) {
	if !config.ValidUnit(unit) {
		return "", fmt.Errorf("非法的 unit 名: %q", unit)
	}
	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, m.Journalctl,
		"-u", unit, "-n", strconv.Itoa(lines), "--no-pager", "--output=short-iso",
	).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("读取日志失败: %v", err)
	}
	return string(out), nil
}

// UnitPath 返回 unit 的真实文件路径（来自 systemctl show 的 FragmentPath）。
func (m *Manager) UnitPath(ctx context.Context, unit string) (string, error) {
	if !config.ValidUnit(unit) {
		return "", fmt.Errorf("非法的 unit 名: %q", unit)
	}
	info, err := m.Inspect(ctx, unit)
	if err != nil {
		return "", err
	}
	if info.LoadState == "not-found" || info.FragmentPath == "" {
		return "", fmt.Errorf("未找到 unit 文件: %s", unit)
	}
	return info.FragmentPath, nil
}

// ReadUnit 读取 unit 文件内容。
func (m *Manager) ReadUnit(ctx context.Context, unit string) (path, content string, err error) {
	p, err := m.UnitPath(ctx, unit)
	if err != nil {
		return "", "", err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", "", err
	}
	if fi.IsDir() {
		return "", "", errors.New("unit 路径是目录")
	}
	if fi.Size() > maxUnitSize {
		return "", "", fmt.Errorf("unit 文件过大（%d 字节，上限 1MB）", fi.Size())
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", "", err
	}
	return p, string(data), nil
}

// WriteUnit 备份后原子写入 unit 文件，并执行 daemon-reload。
func (m *Manager) WriteUnit(ctx context.Context, unit, content string) (saved, backup, reloadOut string, err error) {
	if len(content) > maxUnitSize {
		return "", "", "", fmt.Errorf("内容超过 %d 字节上限", maxUnitSize)
	}
	p, err := m.UnitPath(ctx, unit)
	if err != nil {
		return "", "", "", err
	}
	if !filepath.IsAbs(p) {
		return "", "", "", errors.New("unit 文件路径无效")
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", "", "", err
	}
	if fi.IsDir() {
		return "", "", "", errors.New("unit 路径是目录")
	}
	mode := fi.Mode().Perm()
	uid, gid, hasOwner := fileutil.OwnedBy(fi)

	if berr := copyUnitFile(p, p+".bak"); berr == nil {
		backup = p + ".bak"
	}

	dir := filepath.Dir(p)
	tmp, err := os.CreateTemp(dir, ".pm-unit-*")
	if err != nil {
		return "", "", "", err
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
		return "", "", "", err
	}
	if err = tmp.Chmod(mode); err != nil {
		tmp.Close()
		return "", "", "", err
	}
	if hasOwner {
		fileutil.SetOwner(tmpName, uid, gid)
	}
	if err = tmp.Close(); err != nil {
		return "", "", "", err
	}
	if err = os.Rename(tmpName, p); err != nil {
		return "", "", "", err
	}
	done = true

	reloadOut, err = m.DaemonReload(ctx)
	return p, backup, reloadOut, err
}

// DaemonReload 执行 systemctl daemon-reload。
func (m *Manager) DaemonReload(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, m.Systemctl, "daemon-reload").CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("daemon-reload 失败: %v", err)
	}
	return string(out), nil
}

func copyUnitFile(src, dst string) error {
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

// 调用方负责关闭 reader 并 Wait。
func (m *Manager) StreamLogs(ctx context.Context, unit string, lines int) (*exec.Cmd, io.ReadCloser, error) {
	if !config.ValidUnit(unit) {
		return nil, nil, fmt.Errorf("非法的 unit 名: %q", unit)
	}
	if lines <= 0 {
		lines = 100
	}
	if lines > 1000 {
		lines = 1000
	}

	cmd := exec.CommandContext(ctx, m.Journalctl,
		"-u", unit, "-n", strconv.Itoa(lines), "-f", "--no-pager", "--output=short-iso",
	)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, pipe, nil
}

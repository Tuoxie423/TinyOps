package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Project struct {
	Name string `yaml:"name" json:"name"`
	Desc string `yaml:"desc" json:"desc"`
	Unit string `yaml:"unit" json:"unit"`
	Dir  string `yaml:"dir" json:"dir"`
	// Type: service（默认）或 static
	Type string `yaml:"type" json:"type"`
	// Site 仅静态项目使用，格式 <分组>/<文件>
	Site string `yaml:"site" json:"site"`
}

type NginxDir struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
}

type NginxConfig struct {
	ConfigDir string     `yaml:"config_dir"`
	Binary    string     `yaml:"binary"`
	Service   string     `yaml:"service"`
	Dirs      []NginxDir `yaml:"dirs"`
}

type TerminalConfig struct {
	Shell string `yaml:"shell"`
	Home  string `yaml:"home"`
}

type Config struct {
	Listen       string         `yaml:"listen"`
	Password     string         `yaml:"password"`
	SessionHours int            `yaml:"session_hours"`
	Systemctl    string         `yaml:"systemctl"`
	MaxUploadMB  int            `yaml:"max_upload_mb"`
	Projects     []Project      `yaml:"projects"`
	Nginx        NginxConfig    `yaml:"nginx"`
	Terminal     TerminalConfig `yaml:"terminal"`

	DiscoverProjects *bool    `yaml:"discover_projects"`
	DiscoverExclude  []string `yaml:"discover_exclude"`
}

func (c *Config) ShouldDiscover() bool {
	return c.DiscoverProjects == nil || *c.DiscoverProjects
}

func Merge(lists ...[]Project) []Project {
	var out []Project
	seenUnit := make(map[string]bool)
	seenName := make(map[string]bool)
	for _, list := range lists {
		for _, p := range list {
			if seenName[p.Name] {
				continue
			}
			// 静态项目没有 unit，跳过 unit 去重
			if p.Unit != "" && seenUnit[p.Unit] {
				continue
			}
			out = append(out, p)
			if p.Unit != "" {
				seenUnit[p.Unit] = true
			}
			seenName[p.Name] = true
		}
	}
	return out
}

// 首字符必须是字母或数字，避免被 systemctl 当成选项。
var unitRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]*\.(service|timer|socket|mount|target)$`)

func ValidUnit(unit string) bool { return unitRe.MatchString(unit) }

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("解析 YAML: %w", err)
	}

	if v := os.Getenv("PM_PASSWORD"); v != "" {
		c.Password = v
	}
	if v := os.Getenv("PM_LISTEN"); v != "" {
		c.Listen = v
	}

	if c.Listen == "" {
		c.Listen = "127.0.0.1:4237"
	}
	if c.SessionHours <= 0 {
		c.SessionHours = 24
	}
	if c.MaxUploadMB <= 0 {
		c.MaxUploadMB = 512
	}
	if c.Nginx.ConfigDir == "" {
		c.Nginx.ConfigDir = "/etc/nginx"
	}
	if c.Nginx.Binary == "" {
		c.Nginx.Binary = "/usr/sbin/nginx"
	}
	if c.Nginx.Service == "" {
		c.Nginx.Service = "nginx"
	}
	if c.Terminal.Shell == "" {
		c.Terminal.Shell = "/bin/bash"
	}
	if c.Terminal.Home == "" {
		c.Terminal.Home = os.Getenv("HOME")
		if c.Terminal.Home == "" {
			c.Terminal.Home = "/"
		}
	}
	if c.Systemctl == "" {
		c.Systemctl = lookup("systemctl", "/usr/bin/systemctl")
	}
	if c.Nginx.ConfigDir != "" {
		abs, err := filepath.Abs(c.Nginx.ConfigDir)
		if err != nil {
			return nil, fmt.Errorf("nginx.config_dir 无效: %w", err)
		}
		c.Nginx.ConfigDir = filepath.Clean(abs)
	}
	seenDir := make(map[string]bool, len(c.Nginx.Dirs))
	for i := range c.Nginx.Dirs {
		d := &c.Nginx.Dirs[i]
		if strings.TrimSpace(d.Path) == "" {
			return nil, fmt.Errorf("nginx.dirs[%d] 缺少 path", i)
		}
		if !filepath.IsAbs(d.Path) {
			d.Path = filepath.Join(c.Nginx.ConfigDir, d.Path)
		}
		d.Path = filepath.Clean(d.Path)
		d.Name = strings.TrimSpace(d.Name)
		if d.Name == "" {
			d.Name = filepath.Base(d.Path)
		}
		if d.Name == "all" {
			return nil, fmt.Errorf("nginx.dirs 中 name 不能使用保留值 all")
		}
		if seenDir[d.Name] {
			return nil, fmt.Errorf("nginx.dirs 名称重复: %s", d.Name)
		}
		seenDir[d.Name] = true
	}
	if !ValidUnit(c.Nginx.Service) {
		if ValidUnit(c.Nginx.Service + ".service") {
			c.Nginx.Service += ".service"
		} else {
			return nil, fmt.Errorf("nginx service 配置不合法: %q", c.Nginx.Service)
		}
	}
	if c.Password == "" {
		return nil, fmt.Errorf("未设置登录密码：请在配置文件中设置 password，或通过环境变量 PM_PASSWORD 提供")
	}

	seen := make(map[string]bool, len(c.Projects))
	for i := range c.Projects {
		p := &c.Projects[i]
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			return nil, fmt.Errorf("projects[%d] 缺少 name", i)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("项目名重复: %s", p.Name)
		}
		seen[p.Name] = true

		p.Type = strings.TrimSpace(p.Type)
		if p.Type == "" {
			p.Type = "service"
		}
		p.Site = strings.TrimSpace(p.Site)

		switch p.Type {
		case "service":
			if p.Unit == "" {
				p.Unit = p.Name + ".service"
			}
			if !ValidUnit(p.Unit) {
				return nil, fmt.Errorf("项目 %s 的 unit 名不合法: %q", p.Name, p.Unit)
			}
		case "static":
			p.Unit = ""
			if p.Dir == "" {
				return nil, fmt.Errorf("静态项目 %s 必须配置 dir", p.Name)
			}
			if p.Site != "" && !strings.Contains(p.Site, "/") {
				return nil, fmt.Errorf("静态项目 %s 的 site 格式应为 <分组>/<文件>", p.Name)
			}
		default:
			return nil, fmt.Errorf("项目 %s 的 type 不合法: %q（只支持 service / static）", p.Name, p.Type)
		}

		if p.Dir != "" {
			abs, err := filepath.Abs(p.Dir)
			if err != nil {
				return nil, fmt.Errorf("项目 %s 的目录无效: %w", p.Name, err)
			}
			p.Dir = filepath.Clean(abs)
		}
	}
	return &c, nil
}

func lookup(name, fallback string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return fallback
}

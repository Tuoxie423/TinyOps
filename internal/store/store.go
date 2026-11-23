// Package store 读写 projects.added.json。
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/tuoxie423/tinyops/internal/config"
)

type Store struct {
	path string

	mu       sync.RWMutex
	projects []config.Project
}

func New(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.projects); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) All() []config.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]config.Project(nil), s.projects...)
}

func (s *Store) Add(p config.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, x := range s.projects {
		if x.Name == p.Name {
			return fmt.Errorf("项目名称已存在: %s", p.Name)
		}
		if p.Unit != "" && x.Unit == p.Unit {
			return fmt.Errorf("unit 已存在: %s", p.Unit)
		}
	}

	s.projects = append(s.projects, p)
	if err := s.saveLocked(); err != nil {
		s.projects = s.projects[:len(s.projects)-1]
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.projects, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".projects-added-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	done = true
	return nil
}

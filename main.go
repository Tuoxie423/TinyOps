package main

import (
	"bufio"
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tuoxie423/tinyops/internal/api"
	"github.com/tuoxie423/tinyops/internal/auth"
	"github.com/tuoxie423/tinyops/internal/config"
	"github.com/tuoxie423/tinyops/internal/files"
	"github.com/tuoxie423/tinyops/internal/nginx"
	"github.com/tuoxie423/tinyops/internal/store"
	"github.com/tuoxie423/tinyops/internal/sysinfo"
	"github.com/tuoxie423/tinyops/internal/systemd"
	"github.com/tuoxie423/tinyops/internal/term"
)

//go:embed all:web
var webFiles embed.FS

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	webSub, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatalf("加载前端资源失败: %v", err)
	}

	absCfg, err := filepath.Abs(*cfgPath)
	if err != nil {
		log.Fatalf("解析配置路径失败: %v", err)
	}
	st, err := store.New(filepath.Join(filepath.Dir(absCfg), "projects.added.json"))
	if err != nil {
		log.Fatalf("加载已添加项目失败: %v", err)
	}
	cfg.Projects = config.Merge(cfg.Projects, st.All())

	if cfg.ShouldDiscover() {
		before := len(cfg.Projects)
		cfg.Projects = systemd.Discover(cfg.Projects, cfg.DiscoverExclude)
		if n := len(cfg.Projects) - before; n > 0 {
			log.Printf("自动发现 %d 个自建 systemd 服务", n)
		}
	}

	a := auth.New(cfg.Password, time.Duration(cfg.SessionHours)*time.Hour)
	sd := systemd.New(cfg.Projects, cfg.Systemctl)
	info := sysinfo.New()
	info.Start()
	defer info.Stop()

	ng := nginx.New(cfg.Nginx, cfg.Systemctl)
	fileSvc := files.New(cfg.Projects)
	termSrv := term.New(cfg.Terminal)

	started := time.Now()
	h := api.New(api.Deps{
		Cfg:     cfg,
		Auth:    a,
		Systemd: sd,
		Info:    info,
		Nginx:   ng,
		Files:   fileSvc,
		Term:    termSrv,
		Store:   st,
		StartAt: started,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	mux.Handle("/", http.FileServer(http.FS(webSub)))

	handler := securityHeaders(requestLog(mux))

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatalf("监听 %s 失败: %v", cfg.Listen, err)
	}

	log.Printf("TinyOps 已启动: http://%s  (配置: %s, 项目: %d)", cfg.Listen, *cfgPath, len(cfg.Projects))
	if cfg.Password == "change-me" {
		log.Printf("警告: 仍在使用示例密码 change-me，请立即通过 PM_PASSWORD 环境变量或配置文件修改")
	}
	if !isLoopback(cfg.Listen) {
		log.Printf("警告: 正在监听非本机地址。该面板可以执行命令、覆盖文件，请务必配合 HTTPS/防火墙/强密码使用")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		log.Println("收到退出信号，正在关闭...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("关闭服务: %v", err)
		}
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("服务异常退出: %v", err)
		}
	}
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.HasPrefix(addr, "127.") || addr == "localhost"
	}
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; font-src 'self' data:; connect-src 'self' ws: wss:")
		if !strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/ws/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush / Hijack 必须透传，否则 SSE 和 WebSocket 都会失效。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("当前 ResponseWriter 不支持 Hijack")
	}
	return h.Hijack()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/api/overview" {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.code, time.Since(start).Round(time.Millisecond))
		}
	})
}

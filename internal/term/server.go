package term

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/tuoxie423/tinyops/internal/config"
)

type Server struct {
	shell string
	home  string
	up    websocket.Upgrader
}

func New(c config.TerminalConfig) *Server {
	home := c.Home
	if home == "" {
		home = "/"
	}
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		home = "/"
	}
	return &Server{
		shell: c.Shell,
		home:  home,
		up: websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			// 防跨站劫持，只允许同源页面
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					// 非浏览器客户端没有 Origin
					return true
				}
				u, err := url.Parse(origin)
				if err != nil {
					return false
				}
				return strings.EqualFold(u.Host, r.Host)
			},
		},
	}
}

type inputMsg struct {
	Type string `json:"t"`
	Data string `json:"d"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (s *Server) Handle(w http.ResponseWriter, r *http.Request) {
	conn, err := s.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	s.run(conn)
}

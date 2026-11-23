//go:build windows

package term

import "github.com/gorilla/websocket"

func (s *Server) run(conn *websocket.Conn) {
	conn.WriteMessage(websocket.TextMessage,
		[]byte("\r\n终端功能仅支持 Linux 服务器（当前为 Windows 构建）。\r\n"))
}

//go:build !windows

package term

import (
	"encoding/json"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

func (s *Server) run(conn *websocket.Conn) {
	cmd := exec.Command(s.shell, "-l")
	cmd.Dir = s.home
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("\r\n启动终端失败: "+err.Error()+"\r\n"))
		return
	}
	defer func() {
		ptmx.Close()
		if cmd.Process != nil {
			// Setsid 后 pgid == pid
			syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
			done := make(chan struct{})
			go func() {
				cmd.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}
	}()

	// PTY 输出 -> WebSocket（唯一的写协程）
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				conn.Close()
				return
			}
		}
	}()

	// WebSocket 输入 -> PTY
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.TextMessage {
			var m inputMsg
			if json.Unmarshal(data, &m) == nil {
				switch m.Type {
				case "input":
					if m.Data != "" {
						ptmx.Write([]byte(m.Data))
					}
				case "resize":
					if m.Cols > 0 && m.Rows > 0 {
						pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(m.Cols), Rows: uint16(m.Rows)})
					}
				}
			}
			continue
		}
		ptmx.Write(data)
	}
}

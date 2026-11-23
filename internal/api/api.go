package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tuoxie423/tinyops/internal/auth"
	"github.com/tuoxie423/tinyops/internal/config"
	"github.com/tuoxie423/tinyops/internal/files"
	"github.com/tuoxie423/tinyops/internal/nginx"
	"github.com/tuoxie423/tinyops/internal/store"
	"github.com/tuoxie423/tinyops/internal/sysinfo"
	"github.com/tuoxie423/tinyops/internal/systemd"
	"github.com/tuoxie423/tinyops/internal/term"
)

type Deps struct {
	Cfg     *config.Config
	Auth    *auth.Auth
	Systemd *systemd.Manager
	Info    *sysinfo.Sampler
	Nginx   *nginx.Service
	Files   *files.Service
	Term    *term.Server
	Store   *store.Store
	StartAt time.Time
}

type API struct {
	d Deps
}

func New(d Deps) *API { return &API{d: d} }

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)

	p := http.NewServeMux()
	p.HandleFunc("GET /api/me", a.me)
	p.HandleFunc("GET /api/overview", a.overview)
	p.HandleFunc("GET /api/projects", a.projects)
	p.HandleFunc("POST /api/projects", a.addProject)
	p.HandleFunc("POST /api/projects/{name}/{action}", a.projectAction)
	p.HandleFunc("GET /api/projects/{name}/logs", a.projectLogs)
	p.HandleFunc("GET /api/projects/{name}/logs/stream", a.projectLogStream)

	p.HandleFunc("GET /api/nginx/groups", a.nginxGroups)
	p.HandleFunc("GET /api/nginx/files", a.nginxFiles)
	p.HandleFunc("GET /api/nginx/file", a.nginxRead)
	p.HandleFunc("PUT /api/nginx/file", a.nginxSave)
	p.HandleFunc("POST /api/nginx/enable", a.nginxEnable)
	p.HandleFunc("POST /api/nginx/disable", a.nginxDisable)
	p.HandleFunc("POST /api/nginx/test", a.nginxTest)
	p.HandleFunc("POST /api/nginx/reload", a.nginxReload)

	p.HandleFunc("GET /api/files/list", a.filesList)
	p.HandleFunc("GET /api/files/download", a.filesDownload)
	p.HandleFunc("POST /api/files/upload", a.filesUpload)

	p.HandleFunc("GET /ws/terminal", a.d.Term.Handle)

	mux.Handle("/api/", a.d.Auth.Require(p))
	mux.Handle("/ws/", a.d.Auth.Require(p))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeResult(w http.ResponseWriter, code int, output string, err error) {
	resp := map[string]any{"ok": err == nil, "output": output}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, code, resp)
}

func intQuery(r *http.Request, key string, def int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	return v
}

type loginReq struct {
	Password string `json:"password"`
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r)
	if a.d.Auth.Throttled(ip) {
		writeErr(w, http.StatusTooManyRequests, "尝试过于频繁，请一分钟后再试")
		return
	}

	var req loginReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if !a.d.Auth.CheckPassword(req.Password) {
		a.d.Auth.RecordFailure(ip)
		time.Sleep(300 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "密码错误")
		return
	}
	a.d.Auth.ClearFailures(ip)
	if err := a.d.Auth.NewSession(w); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	a.d.Auth.Logout(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"started":   a.d.StartAt,
		"projects":  len(a.d.Systemd.Projects()),
		"nginx_dir": a.d.Nginx.Dir(),
	})
}

func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.d.Info.Snapshot())
}

func (a *API) projects(w http.ResponseWriter, r *http.Request) {
	list := a.d.Systemd.Projects()
	out := make([]systemd.Status, len(list))

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, p := range list {
		if p.Type == "static" {
			out[i] = a.staticStatus(p)
			continue
		}
		wg.Add(1)
		go func(i int, p config.Project) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = a.d.Systemd.Status(r.Context(), p)
		}(i, p)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

func (a *API) staticStatus(p config.Project) systemd.Status {
	st := systemd.Status{
		Name: p.Name,
		Desc: p.Desc,
		Dir:  p.Dir,
		Type: "static",
		Site: p.Site,
	}
	if p.Site == "" {
		st.State = "未关联站点"
		st.Err = "该项目没有关联 nginx 配置，无法上线/下线"
		return st
	}
	online, err := a.d.Nginx.SiteEnabled(p.Site)
	if err != nil {
		st.State = "未知"
		st.Err = err.Error()
		return st
	}
	st.Active = online
	if online {
		st.State = "在线"
		st.Enabled = "enabled"
	} else {
		st.State = "已下线"
		st.Enabled = "disabled"
	}
	return st
}

type addProjectReq struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
	Desc string `json:"desc"`
	Dir  string `json:"dir"`
	Type string `json:"type"`
	Site string `json:"site"`
}

func (a *API) addProject(w http.ResponseWriter, r *http.Request) {
	var req addProjectReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "添加失败：请求格式错误")
		return
	}

	kind := strings.TrimSpace(req.Type)
	if kind == "" {
		kind = "service"
	}
	if kind != "service" && kind != "static" {
		writeErr(w, http.StatusBadRequest, "添加失败：项目类型不合法（只支持 service / static）")
		return
	}

	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "添加失败：项目目录不能为空")
		return
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "添加失败：项目目录无效")
		return
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		writeErr(w, http.StatusBadRequest, "添加失败：目录不存在或不是目录: "+abs)
		return
	}

	var unit, site string
	if kind == "service" {
		unit = strings.TrimSpace(req.Unit)
		if unit == "" {
			writeErr(w, http.StatusBadRequest, "添加失败：service 名称不能为空")
			return
		}
		if !strings.Contains(unit, ".") {
			unit += ".service"
		}
		if !config.ValidUnit(unit) {
			writeErr(w, http.StatusBadRequest, "添加失败：service 名称不合法")
			return
		}
	} else {
		index := filepath.Join(abs, "index.html")
		idxFi, ierr := os.Stat(index)
		if ierr != nil || !idxFi.Mode().IsRegular() {
			writeErr(w, http.StatusBadRequest, "添加失败：静态项目目录根部必须有 index.html")
			return
		}

		site = strings.TrimSpace(req.Site)
		if site == "" {
			candidates := a.d.Nginx.MatchSiteForDir(filepath.Clean(abs))
			switch len(candidates) {
			case 0:
				writeErr(w, http.StatusBadRequest,
					"添加失败：未找到 root 指向该目录的 nginx 配置，请填写站点配置（如 sites-available/xxx.conf）")
				return
			case 1:
				site = candidates[0]
			default:
				writeErr(w, http.StatusBadRequest,
					"添加失败：找到多个引用该目录的 nginx 配置，请填写其中一个："+strings.Join(candidates, "、"))
				return
			}
		}
		normalized, nerr := a.d.Nginx.NormalizeSite(site)
		if nerr != nil {
			writeErr(w, http.StatusBadRequest, "添加失败：nginx 站点无效："+nerr.Error())
			return
		}
		site = normalized
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		if kind == "service" {
			name = strings.TrimSuffix(unit, ".service")
		} else {
			name = filepath.Base(filepath.Clean(abs))
		}
	}
	if strings.ContainsAny(name, `/\`) || len([]rune(name)) > 64 {
		writeErr(w, http.StatusBadRequest, "添加失败：项目名称不合法")
		return
	}

	for _, p := range a.d.Systemd.Projects() {
		if p.Name == name {
			writeErr(w, http.StatusConflict, "添加失败：项目名称已存在: "+name)
			return
		}
		if unit != "" && p.Unit == unit {
			writeErr(w, http.StatusConflict, "添加失败：该 service 已在项目列表中: "+unit)
			return
		}
		if site != "" && p.Site == site {
			writeErr(w, http.StatusConflict, "添加失败：该 nginx 站点已被项目 "+p.Name+" 使用")
			return
		}
	}

	desc := strings.TrimSpace(req.Desc)
	if kind == "service" {
		info, ierr := a.d.Systemd.Inspect(r.Context(), unit)
		if ierr != nil {
			writeErr(w, http.StatusBadGateway, "添加失败："+ierr.Error())
			return
		}
		if info.LoadState == "" || info.LoadState == "not-found" {
			writeErr(w, http.StatusNotFound, "添加失败：未找到 service "+unit)
			return
		}
		if desc == "" {
			desc = strings.TrimSpace(info.Description)
		}
	}

	p := config.Project{
		Name: name,
		Desc: desc,
		Unit: unit,
		Dir:  filepath.Clean(abs),
		Type: kind,
		Site: site,
	}

	if err := a.d.Store.Add(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "添加失败：保存项目失败: "+err.Error())
		return
	}
	if err := a.d.Systemd.Add(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "添加失败："+err.Error())
		return
	}
	if err := a.d.Files.Add(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "添加失败："+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project": p})
}

func (a *API) projectAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	action := r.PathValue("action")

	p, ok := a.d.Systemd.Find(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "项目不存在: "+name)
		return
	}

	if p.Type == "static" {
		a.staticAction(w, r, p, action)
		return
	}
	if action == "online" || action == "offline" {
		writeErr(w, http.StatusBadRequest, "后端服务不支持上线/下线操作")
		return
	}

	output, err := a.d.Systemd.Action(r.Context(), p.Unit, action)
	st := a.d.Systemd.Status(r.Context(), p)

	resp := map[string]any{"ok": err == nil, "output": output, "status": st}
	if err != nil {
		resp["error"] = err.Error()
		writeJSON(w, http.StatusBadGateway, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) staticAction(w http.ResponseWriter, r *http.Request, p config.Project, action string) {
	if action != "online" && action != "offline" {
		writeErr(w, http.StatusBadRequest, "静态项目只支持上线 / 下线操作")
		return
	}
	if p.Site == "" {
		writeErr(w, http.StatusBadRequest, "该项目未关联 nginx 站点，无法上线/下线")
		return
	}

	var output string
	var err error
	if action == "offline" {
		output, err = a.d.Nginx.OfflineSite(p.Site)
	} else {
		output, err = a.d.Nginx.OnlineSite(p.Site)
	}

	st := a.staticStatus(p)
	resp := map[string]any{"ok": err == nil, "output": output, "status": st}
	if err != nil {
		resp["error"] = err.Error()
		writeJSON(w, http.StatusBadGateway, resp)
		return
	}

	testOutput, testErr := a.d.Nginx.Test(r.Context())
	resp["test_ok"] = testErr == nil
	resp["test_output"] = testOutput
	if testErr != nil {
		resp["test_error"] = testErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) projectLogs(w http.ResponseWriter, r *http.Request) {
	p, ok := a.d.Systemd.Find(r.PathValue("name"))
	if !ok {
		writeErr(w, http.StatusNotFound, "项目不存在")
		return
	}
	lines := intQuery(r, "lines", 200)
	out, err := a.d.Systemd.Logs(r.Context(), p.Unit, lines)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (a *API) projectLogStream(w http.ResponseWriter, r *http.Request) {
	p, ok := a.d.Systemd.Find(r.PathValue("name"))
	if !ok {
		writeErr(w, http.StatusNotFound, "项目不存在")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "当前服务器不支持流式输出")
		return
	}

	cmd, rc, err := a.d.Systemd.StreamLogs(r.Context(), p.Unit, intQuery(r, "lines", 100))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "启动 journalctl 失败: "+err.Error())
		return
	}
	defer func() {
		rc.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, ": connected\n\n")
	flusher.Flush()

	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(w, "event: end\ndata: %s\n\n", err.Error())
	} else {
		io.WriteString(w, "event: end\ndata: journalctl 已退出\n\n")
	}
	flusher.Flush()
}

func (a *API) nginxGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"dir":    a.d.Nginx.Dir(),
		"groups": a.d.Nginx.Groups(),
	})
}

func (a *API) nginxFiles(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	files, err := a.d.Nginx.List(group)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": group, "files": files})
}

func (a *API) nginxRead(w http.ResponseWriter, r *http.Request) {
	res, err := a.d.Nginx.Read(r.URL.Query().Get("group"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type nginxSaveReq struct {
	Group   string `json:"group"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (a *API) nginxSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)

	var req nginxSaveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}

	saved, backup, err := a.d.Nginx.Save(req.Group, req.Path, req.Content)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	output, testErr := a.d.Nginx.Test(r.Context())
	resp := map[string]any{
		"ok":          true,
		"saved":       saved,
		"backup":      backup,
		"test_ok":     testErr == nil,
		"test_output": output,
	}
	if testErr != nil {
		resp["test_error"] = testErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

type nginxSiteReq struct {
	Group string `json:"group"`
	Path  string `json:"path"`
}

func (a *API) nginxEnable(w http.ResponseWriter, r *http.Request) {
	var req nginxSiteReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	link, err := a.d.Nginx.EnableSite(req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeNginxSiteResult(w, r, map[string]any{"ok": true, "link": link})
}

func (a *API) nginxDisable(w http.ResponseWriter, r *http.Request) {
	var req nginxSiteReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	removed, err := a.d.Nginx.DisableSite(req.Group, req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeNginxSiteResult(w, r, map[string]any{"ok": true, "removed": removed})
}

func (a *API) writeNginxSiteResult(w http.ResponseWriter, r *http.Request, resp map[string]any) {
	output, testErr := a.d.Nginx.Test(r.Context())
	resp["test_ok"] = testErr == nil
	resp["test_output"] = output
	if testErr != nil {
		resp["test_error"] = testErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) nginxTest(w http.ResponseWriter, r *http.Request) {
	out, err := a.d.Nginx.Test(r.Context())
	writeResult(w, http.StatusOK, out, err)
}

func (a *API) nginxReload(w http.ResponseWriter, r *http.Request) {
	out, err := a.d.Nginx.Reload(r.Context())
	writeResult(w, http.StatusOK, out, err)
}

func (a *API) filesList(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	rel := r.URL.Query().Get("path")
	entries, err := a.d.Files.List(project, rel)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "path": rel, "entries": entries})
}

func (a *API) filesDownload(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	rel := r.URL.Query().Get("path")
	abs, err := a.d.Files.Abs(project, rel)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		writeErr(w, http.StatusNotFound, "文件不存在")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, abs)
}

func (a *API) filesUpload(w http.ResponseWriter, r *http.Request) {
	maxBytes := int64(a.d.Cfg.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("文件超过上传上限（%d MB）", a.d.Cfg.MaxUploadMB))
			return
		}
		writeErr(w, http.StatusBadRequest, "解析上传内容失败: "+err.Error())
		return
	}

	project := r.FormValue("project")
	rel := r.FormValue("path")
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "缺少 file 字段")
		return
	}
	defer file.Close()

	if rel == "" {
		rel = header.Filename
	}

	saved, backup, size, err := a.d.Files.Save(project, rel, file)
	if err != nil {
		code := http.StatusBadRequest
		if os.IsPermission(err) {
			code = http.StatusForbidden
		}
		writeErr(w, code, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"saved":  saved,
		"size":   size,
		"backup": backup,
	})
}

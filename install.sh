#!/usr/bin/env bash
set -euo pipefail

SERVICE="projectmanager"
DEFAULT_BIN="/opt/projectmanager/projectmanager"
DEFAULT_CFG="/etc/projectmanager/config.yaml"
DEFAULT_UNIT="/etc/systemd/system/projectmanager.service"

die() { echo "错误: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "请使用 root 运行"
command -v systemctl >/dev/null 2>&1 || die "没有 systemctl"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_SRC="$SCRIPT_DIR/projectmanager"
CFG_SRC="$SCRIPT_DIR/config.example.yaml"
UNIT_SRC="$SCRIPT_DIR/deploy/projectmanager.service"

for f in "$BIN_SRC" "$CFG_SRC" "$UNIT_SRC"; do
    [ -f "$f" ] || die "发布包不完整，缺少 $f"
done

BIN="$DEFAULT_BIN"
CFG="$DEFAULT_CFG"
UNIT="$DEFAULT_UNIT"

if [ -f "$UNIT" ]; then
    exec_line=$(grep -E '^ExecStart=' "$UNIT" | head -n1 | sed 's/^ExecStart=//')
    parsed_bin=$(printf '%s' "$exec_line" | awk '{print $1}')
    parsed_cfg=$(printf '%s' "$exec_line" | sed -n 's/.*-config[= ]\{1,\}\([^ ]*\).*/\1/p')
    [ -n "$parsed_bin" ] && BIN="$parsed_bin"
    [ -n "$parsed_cfg" ] && CFG="$parsed_cfg"
    echo "检测到已有 unit，沿用其中路径"
else
    echo "首次安装，使用默认路径"
fi
echo "  二进制: $BIN"
echo "  配置:   $CFG"

install -d -m 755 "$(dirname "$BIN")" "$(dirname "$CFG")"

if [ -f "$BIN" ]; then
    cp -a "$BIN" "$BIN.bak"
fi
install -m 755 "$BIN_SRC" "$BIN"

if [ -f "$CFG" ]; then
    echo "保留现有配置: $CFG"
else
    install -m 600 "$CFG_SRC" "$CFG"
    echo "已创建配置: $CFG"
fi

# 密码优先级：配置文件 > env 文件 > PM_PASSWORD > 交互输入 > 随机生成
cfg_password=$(sed -n 's/^password:[[:space:]]*//p' "$CFG" | head -n1 | tr -d "\"'" | sed 's/[[:space:]]*#.*//')
password_ok=0
if [ -n "$cfg_password" ] && [ "$cfg_password" != "change-me" ]; then
    password_ok=1
fi

env_file=""
has_env_directive=0
if [ -f "$UNIT" ]; then
    env_line=$(grep -E '^EnvironmentFile=' "$UNIT" | head -n1 | sed 's/^EnvironmentFile=//')
    if [ -n "$env_line" ]; then
        has_env_directive=1
        env_file="${env_line#-}"
    fi
fi
[ -z "$env_file" ] && env_file="$(dirname "$CFG")/env"

if [ "$password_ok" -eq 0 ] && [ -f "$env_file" ] && grep -q '^PM_PASSWORD=' "$env_file"; then
    password_ok=1
fi

if [ "$password_ok" -eq 0 ]; then
    [ -f "$UNIT" ] && [ "$has_env_directive" -eq 0 ] && \
        die "现有 unit 没有 EnvironmentFile，请把密码写进 $CFG"

    pw="${PM_PASSWORD:-}"
    if [ -z "$pw" ] && [ -t 0 ]; then
        read -rsp "设置登录密码: " pw; echo
        read -rsp "确认密码: " pw2; echo
        [ "$pw" = "$pw2" ] || die "两次密码不一致"
    fi
    if [ -z "$pw" ]; then
        pw=$(head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 20)
        echo "未提供密码，已生成: $pw"
    fi
    [ -n "$pw" ] || die "密码不能为空"
    install -d -m 755 "$(dirname "$env_file")"
    esc=${pw//\\/\\\\}
    esc=${esc//\"/\\\"}
    umask 077
    printf 'PM_PASSWORD="%s"\n' "$esc" > "$env_file"
    chmod 600 "$env_file"
    echo "密码已写入: $env_file"
fi

if [ ! -f "$UNIT" ]; then
    install -m 644 "$UNIT_SRC" "$UNIT"
    echo "已安装 unit: $UNIT"
else
    echo "保留现有 unit: $UNIT"
fi

systemctl daemon-reload
systemctl enable "$SERVICE" >/dev/null 2>&1 || true

rollback() {
    command -v journalctl >/dev/null 2>&1 && journalctl -u "$SERVICE" -n 30 --no-pager || true
    if [ -f "$BIN.bak" ]; then
        echo "回滚旧二进制"
        install -m 755 "$BIN.bak" "$BIN"
        systemctl restart "$SERVICE" || true
    fi
}

if ! systemctl restart "$SERVICE"; then
    echo "服务重启失败，最近日志:"
    rollback
    exit 1
fi

sleep 1
if ! systemctl is-active --quiet "$SERVICE"; then
    echo "服务未运行，最近日志:"
    rollback
    exit 1
fi

listen=""
if [ -f "$env_file" ]; then
    listen=$(sed -n 's/^PM_LISTEN=//p' "$env_file" | head -n1 | tr -d "\"'")
fi
if [ -z "$listen" ]; then
    listen=$(sed -n 's/^listen:[[:space:]]*//p' "$CFG" | head -n1 | tr -d "\"'" | sed 's/[[:space:]]*#.*//')
fi
host="${listen%:*}"
port="${listen##*:}"
[ "$host" = "0.0.0.0" ] && host="127.0.0.1"
[ -z "$host" ] && host="127.0.0.1"
case "$host" in
    *:*) url_host="[$host]" ;;
    *) url_host="$host" ;;
esac

if command -v curl >/dev/null 2>&1; then
    code=$(curl -s -o /dev/null -m 3 -w '%{http_code}' "http://$url_host:$port/api/me" || true)
    case "$code" in
        200|401) echo "健康检查通过 (HTTP $code)" ;;
        *) echo "警告: 健康检查返回 '$code'，请确认监听地址和端口" ;;
    esac
else
    echo "未安装 curl，跳过健康检查"
fi

echo
echo "TinyOps 安装完成"
echo "  服务:   $SERVICE"
echo "  二进制: $BIN"
echo "  配置:   $CFG"
echo "  访问:   ssh -N -L $port:127.0.0.1:$port <user>@<server>"
echo "          http://127.0.0.1:$port"

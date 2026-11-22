package drivers

import (
	"slices"
	"strings"

	"github.com/hakastein/smith/broker"
)

// Окружение запуска — белый список: токены сервиса и всё прочее, чего здесь нет, в процесс модели не попадает.
// Нужное сверх этого драйверу задаёт Settings.Env.
var baseEnv = []string{"PATH", "HOME", "LANG", "TZ", "TMPDIR", "USER", "SHELL", "TERM",
	// сеть и доверие: без них драйвер за прокси не достучится до своего API
	"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy",
	"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR"}

// Environ — окружение запуска: базовые переменные, переменные входа драйвера auth,
// каталог команд агента первым в PATH и переменные из настроек драйвера.
func (s Setup) Environ(settings Settings, auth []string) []string {
	var out []string
	for _, name := range s.EnvOrder {
		if slices.Contains(baseEnv, name) || strings.HasPrefix(name, "LC_") || slices.Contains(auth, name) {
			if name != "PATH" {
				out = append(out, name+"="+s.Env[name])
			}
		}
	}
	out = append(out, "PATH="+s.StageBin+":"+s.Env["PATH"])
	for _, kv := range settings.Env {
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}

// StageEnv — переменные запуска сверх окружения драйвера: Env и путь к сокету команд агента.
func (a Args) StageEnv() []string {
	if a.Socket == "" {
		return a.Env
	}
	return append(slices.Clone(a.Env), broker.SocketEnv+"="+a.Socket)
}

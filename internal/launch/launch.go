// Package launch — то, что драйверы делают одинаково: проверка бинарника, свод потока запуска и файлы запуска.
package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/hakastein/smith/agent"
	"github.com/hakastein/smith/drivers"
)

// BinaryGap — пробел, если бинарник драйвера name не отвечает на --version.
func BinaryGap(ctx context.Context, name string, settings drivers.Settings) []drivers.Gap {
	cmd := exec.CommandContext(ctx, settings.Bin, "--version")
	agent.Group(cmd)
	err := cmd.Run()
	if err == nil {
		return nil
	}
	reason := ""
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		reason = fmt.Sprintf("код %d", exit.ExitCode())
	default:
		reason = err.Error()
	}
	return []drivers.Gap{{ID: "driver-" + name, Message: fmt.Sprintf("драйвер %s: %s не запускается (%s)", name, settings.Bin, reason)}}
}

// HomeDir — HOME окружения прогона, без него — HOME процесса.
func HomeDir(env map[string]string) string {
	if h := env["HOME"]; h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

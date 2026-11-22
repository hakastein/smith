// Package logx — лог прогона в stderr с отметкой времени от начала прогона, без секретов окружения: stdout
// процесса остаётся его результату.
package logx

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hakastein/smith/redact"
)

// StartedAt — начало прогона.
var StartedAt = time.Now()

var mu sync.Mutex

// prefix — метка строк Log.
var prefix = "[smith] "

// SetPrefix задаёт метку строк Log: имя сервиса в квадратных скобках с пробелом.
func SetPrefix(p string) {
	mu.Lock()
	defer mu.Unlock()
	prefix = p
}

// SinceStart — миллисекунды от начала прогона.
func SinceStart() int64 { return time.Since(StartedAt).Milliseconds() }

func elapsed() string {
	return fmt.Sprintf("%5d", (SinceStart()+500)/1000)
}

// Stamped пишет строки текста с отметкой времени. Вымарывание здесь, а не у
// вызывающих: в лог идут текст модели, argv и ошибки.
func Stamped(text string) {
	var b strings.Builder
	for _, line := range strings.Split(redact.Secrets(text), "\n") {
		fmt.Fprintf(&b, "[%ss] %s\n", elapsed(), line)
	}
	mu.Lock()
	defer mu.Unlock()
	os.Stderr.WriteString(b.String())
}

// Log — строка лога с меткой SetPrefix.
func Log(message string) {
	mu.Lock()
	p := prefix
	mu.Unlock()
	Stamped(p + message)
}

// Logf — Log с форматом.
func Logf(format string, args ...any) { Log(fmt.Sprintf(format, args...)) }

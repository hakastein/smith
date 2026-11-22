// Package broker — команды агента на стороне сервиса. В песочнице у агента только тонкий клиент (Main): он
// передаёт argv и рабочий каталог по unix-сокету этапа, сервис исполняет вызов Go-обработчиком и возвращает
// выдачу. Один бинарь клиента ставится под именем каждой команды: имя, под которым он вызван, — первый элемент argv.
package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SocketEnv — переменная окружения агента с путём к сокету этапа; её ставит драйвер.
const SocketEnv = "SMITH_SOCKET"

// Call — вызов команды агентом: argv с именем команды первым и рабочий каталог вызова.
type Call struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
}

// Reply — выдача команды агенту: stdout, stderr и код выхода клиента.
type Reply struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
}

// Main — клиент: name — имя, под которым вызван бинарь, args — аргументы после него.
func Main(name string, args []string, stdout, stderr io.Writer) int {
	socket := os.Getenv(SocketEnv)
	if socket == "" {
		fmt.Fprintf(stderr, "%s: нет %s — команда работает только внутри этапа агента\n", name, SocketEnv)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "%s: рабочий каталог не прочитан: %v\n", name, err)
		return 2
	}
	r, err := Do(context.Background(), socket, Call{Argv: append([]string{filepath.Base(name)}, args...), Cwd: cwd})
	if err != nil {
		fmt.Fprintf(stderr, "%s: сервис не ответил: %v\n", name, err)
		return 2
	}
	_, _ = io.WriteString(stdout, r.Stdout)
	_, _ = io.WriteString(stderr, r.Stderr)
	return r.Exit
}

// Do передаёт вызов сервису по сокету этапа.
func Do(ctx context.Context, socket string, c Call) (Reply, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return Reply{}, err
	}
	client := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://harness/call", bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return Reply{}, fmt.Errorf("%s: %s", resp.Status, msg)
	}
	var r Reply
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

// Socket — сокет команд этапа в своём временном каталоге. Каталог открывают на чтение только песочнице этого
// этапа: агент не зовёт команды через сокет соседнего этапа от его имени.
type Socket struct {
	Path string // путь к сокету: drivers.Args.Socket
	dir  string
	srv  *http.Server
}

// Listen обслуживает вызовы этапа на новом сокете, пока не вызван Close. Вызов, argv которого не начинается словами
// одного из префиксов allow (`notes post`), отклоняется кодом 2, handle его не видит.
func Listen(allow []string, handle func(context.Context, Call) Reply) (*Socket, error) {
	// shortcut: путь до сокета не длиннее 104 байт (предел macOS) только при коротком TMPDIR; переносить, если упрётся.
	dir, err := os.MkdirTemp("", "smith-sock-")
	if err != nil {
		return nil, err
	}
	s := &Socket{Path: filepath.Join(dir, "commands.sock"), dir: dir}
	l, err := net.Listen("unix", s.Path)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("сокет команд %s: %w", s.Path, err), os.RemoveAll(dir))
	}
	s.srv = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c Call
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil || len(c.Argv) == 0 {
			http.Error(w, "нужен JSON {argv, cwd}", http.StatusBadRequest)
			return
		}
		reply := Reply{Stderr: fmt.Sprintf("команда %q этапу не разрешена\n", strings.Join(c.Argv, " ")), Exit: 2}
		if allowed(allow, c.Argv) {
			reply = handle(r.Context(), c)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	})}
	// Сбой листенера агент увидит отказом вызова.
	go func() { _ = s.srv.Serve(l) }()
	return s, nil
}

// Close останавливает сокет и удаляет его каталог.
func (s *Socket) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
	return os.RemoveAll(s.dir)
}

// allowed — argv начинается словами одного из непустых префиксов allow.
func allowed(allow, argv []string) bool {
	return slices.ContainsFunc(allow, func(prefix string) bool {
		words := strings.Fields(prefix)
		return len(words) > 0 && len(words) <= len(argv) && slices.Equal(words, argv[:len(words)])
	})
}

// connect-dev serves only the local UI API; it never launches a client.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"surplustoken-connect/internal/configuration"
	"surplustoken-connect/internal/discovery"
	"surplustoken-connect/internal/gateway"
	"surplustoken-connect/internal/httpapi"
	"time"
)

type options struct {
	fixtureDir string
	mock       bool
	listen     string
}

type readyMessage struct {
	Address    string `json:"address"`
	FixtureDir string `json:"fixture_dir"`
	Mock       bool   `json:"mock"`
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("connect-dev", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&opts.fixtureDir, "fixture-dir", "", "配置写入测试目录（不使用真实客户端配置）")
	flags.BoolVar(&opts.mock, "mock", false, "使用离线网关夹具（必须指定 fixture-dir）")
	flags.StringVar(&opts.listen, "listen", "127.0.0.1:43129", "仅回环 IP 监听地址；127.0.0.1:0 自动分配端口")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, errors.New("不接受位置参数")
	}
	if opts.mock && opts.fixtureDir == "" {
		return opts, errors.New("离线夹具必须指定 fixture-dir")
	}
	host, port, err := net.SplitHostPort(opts.listen)
	if err != nil {
		return opts, errors.New("监听地址必须是回环 IP 和端口")
	}
	ip := net.ParseIP(host)
	number, portErr := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || portErr != nil || number < 0 || number > 65535 {
		return opts, errors.New("监听地址必须使用回环 IP 和 0–65535 端口，不接受主机名或外部地址")
	}
	return opts, nil
}

func fixtureEngine(directory string) (*configuration.Engine, string, error) {
	root, err := filepath.Abs(directory)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, "", err
	}
	engine, err := configuration.New(filepath.Join(root, "backups"), map[string]string{
		"claude": filepath.Join(root, "claude", "settings.json"), "opencode": filepath.Join(root, "opencode", "opencode.jsonc"),
	})
	if err != nil {
		return nil, "", err
	}
	engine.Discover = func() map[string]discovery.Result { return map[string]discovery.Result{} }
	return engine, root, nil
}

func run(ctx context.Context, args []string, output io.Writer) error {
	opts, err := parseOptions(args, output)
	if err != nil {
		return err
	}
	// Bind before creating fixture files so a port conflict has no file effects.
	listener, err := net.Listen("tcp", opts.listen)
	if err != nil {
		return fmt.Errorf("无法监听开发服务: %w", err)
	}
	defer listener.Close()

	var engine *configuration.Engine
	var root string
	if opts.fixtureDir != "" {
		engine, root, err = fixtureEngine(opts.fixtureDir)
		if err != nil {
			return fmt.Errorf("测试目录不可用: %w", err)
		}
	} else {
		engine, err = configuration.Default()
	}
	if err != nil {
		return errors.New("无法初始化本地配置服务")
	}
	g := gateway.New()
	if opts.mock {
		remote := mockGateway()
		defer remote.Close()
		g.Base = remote.URL
	}
	api := httpapi.New(g, engine)
	api.Fixture = opts.fixtureDir != ""
	server := &http.Server{Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	ready := readyMessage{Address: "http://" + listener.Addr().String(), FixtureDir: root, Mock: opts.mock}
	encoded, err := json.Marshal(ready)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "CONNECT_DEV_READY %s\n", encoded); err != nil {
		return err
	}
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return err
		}
		return nil
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Fatal(err)
	}
}

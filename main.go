package main

import (
	"embed"
	"flag"
	"log"
	"os"
	"path/filepath"
	"surplustoken-connect/internal/configuration"
	"surplustoken-connect/internal/discovery"
	"surplustoken-connect/internal/gateway"
	"surplustoken-connect/internal/httpapi"
	"surplustoken-connect/internal/sessionstore"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/darwin/AppIcon.png
var appIcon []byte

func main() {
	fixture := flag.String("fixture-dir", "", "配置测试目录（禁用系统登录存储与真实客户端检测）")
	flag.Parse()
	var engine *configuration.Engine
	var err error
	var webviewData string
	if *fixture == "" {
		engine, err = configuration.Default()
	} else {
		if err = os.MkdirAll(*fixture, 0700); err != nil {
			log.Fatal("测试目录不可用")
		}
		root, resolveErr := filepath.EvalSymlinks(*fixture)
		if resolveErr != nil {
			log.Fatal("测试目录不可用")
		}
		webviewData = filepath.Join(root, "webview2")
		engine, err = configuration.New(filepath.Join(root, "backups"), map[string]string{
			"claude":   filepath.Join(root, "claude", "settings.json"),
			"opencode": filepath.Join(root, "opencode", "opencode.jsonc"),
		})
		if err == nil {
			engine.Discover = func() map[string]discovery.Result { return map[string]discovery.Result{} }
		}
	}
	if err != nil {
		log.Fatal("无法初始化本地配置服务")
	}
	api := httpapi.New(gateway.New(), engine)
	api.Fixture = *fixture != ""
	if !api.Fixture {
		api.Store = sessionstore.System{}
	}
	app := application.New(application.Options{
		Name:        "SurplusToken Connect",
		Icon:        appIcon,
		Description: "连接你的 AI 编程工具",
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Services:    []application.Service{application.NewService(httpapi.NewBridge(api))},
		Mac:         application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
		Windows:     application.WindowsOptions{WebviewUserDataPath: webviewData},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "SurplusToken Connect",
		Width: 880, Height: 640, MinWidth: 820, MinHeight: 600,
		BackgroundColour: application.NewRGB(255, 255, 255),
		URL:              "/",
	})
	// All writes require a server-issued preview plan; Codex remains protected.
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

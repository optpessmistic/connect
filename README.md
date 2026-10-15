# SurplusToken Connect

使用 **Go + Wails v3** 构建的轻量桌面接入助手，将基于 Sub2API 的 SurplusToken 网关连接到用户已有的 AI 编程客户端。

目标体验：**登录网关 → 获取或创建 API Key → 选择客户端与 Key → 临时启动或永久配置。**

> 当前已接入 SurplusToken 真实登录、用户资料、分组、Key 列表/创建和模型列表。首批永久配置适配器为 Claude Code 与 OpenCode v2，支持脱敏预览、私密备份、写入与按字段恢复；Codex 保持保护，oh-my-pi、Kimi 和临时终端启动待接入。macOS 构建和临时目录联调已有验证记录；Windows 已通过构建、原生登录页初始化、独立凭据库测试与离线配置/恢复联调，真实客户端推理仍待验收。

五种客户端已接入本机安装与版本检测，包含 GUI 常见安装路径补查、npm 包版本读取和限时版本探测。点击版本号查看详情，和配置适配状态独立展示。Codex 仅做被动安装元数据检查，同时展示 ChatGPT Desktop 应用版本，继续禁止启动或改写。详见 [本机客户端发现](docs/client-discovery.md) 和 [OpenCode v2 升级记录](docs/opencode-v2-migration.md)。

![真实 API 接入界面](docs/screenshots/api-live.jpg)

旧版与 Kumo 版均保留供对比，见 [界面与打包体积对比](docs/ui-comparison.md)。

SuSu 品牌原图与生成说明见 [品牌资源](assets/branding/README.md)，本轮 [Kumo 规范审查](docs/kumo-audit.md)记录已修正问题。

## 产品目标

- 首版在 Connect 中输入网关账号密码，直接调用 HTTPS API 登录；暂不使用网页回调或本地监听。
- 检测可用 Key 与分组，优先复用合适的 Key；没有可用 Key 时，在用户授权的接入流程内创建专用 Key。
- 自动发现已安装客户端，获取模型目录，按客户端协议筛选模型，并允许用户覆盖推荐选择。
- 提供临时启动与永久配置两种模式，按客户端选择，不强制统一。
- 配置前展示脱敏变更预览，自动备份，支持恢复与冲突检测。
- 推理请求由客户端直接发送到网关，无常驻本地模型代理、无本地协议转换。

不纳入首版：完整网关管理后台、MCP/Skills 市场、会话管理、多供应商自动路由、客户端二进制修改。

## 两种接入模式

| | 临时启动 | 永久配置 |
| --- | --- | --- |
| 使用方式 | 从 Connect 或配套 CLI 启动客户端 | 配置后正常打开原有客户端 |
| 配置作用域 | 子进程环境、启动参数或必要的临时文件 | 客户端原生配置与凭据存储 |
| 原有配置 | 不改写用户级配置，检测覆盖冲突 | 仅修改选定字段，保留其他设置 |
| 生命周期 | 启动配置随进程结束失效，清理临时文件 | 重启后仍有效 |
| 卸载 Connect | 无法再通过 Connect 启动 | 已配置客户端应继续可用 |

临时启动仍可在 Connect 自己的凭据库中保存 Key；“临时”指客户端配置作用域，不等于完全不落盘。永久配置不能依赖卸载后消失的 Connect 凭据 helper。若客户端必须将 Key 写入普通文件，应在应用配置时清楚展示其存储位置。

## 目标客户端

下表为完整目标。当前只开放 Claude Code 和 OpenCode v2 的永久配置，其他能力仍待接入。配置位置是调研入口，实际实现需识别版本、环境变量覆盖与自定义目录。

| 客户端 | 临时启动计划 | 永久配置计划 / 核实重点 |
| --- | --- | --- |
| Codex CLI | 进程环境与 CLI 配置覆盖 | 用户级 `~/.codex/config.toml` 与匹配的认证机制；原生 Responses 协议 |
| Codex Desktop / ChatGPT Desktop 中的 Codex | 单独验证应用启动、已有进程复用及环境继承；不预先保证 | 独立测试桌面版本、凭据读取、模型选择和实际请求路由 |
| Claude Code | 环境变量与主/辅助模型配置 | 原生 settings 与认证配置；检查既有设置是否覆盖注入 |
| oh-my-pi（`omp`） | 根据自身启动接口实现 | 当前上游使用 `~/.omp/agent/models.yml` 与 `config.yml`，不能套用 Pi 适配器 |
| OpenCode | 根据上游配置注入接口实现 | 合并用户配置，保留其他 provider、agent 与工具设置 |
| Kimi Code | 面向新 `MoonshotAI/kimi-code` 验证 | 当前文档为 `~/.kimi-code/config.toml`，支持 `KIMI_CODE_HOME`；区分已归档的旧版 |

Codex 接入仅针对编程功能，不意味着替换 ChatGPT 普通聊天后端。官方配置支持自定义 provider，但配置文件可写不等于所有桌面入口都验证通过。桌面凭据不能只依赖 shell 初始化文件中的 `export`。参见 [Codex 配置参考](https://learn.chatgpt.com/docs/config-file/config-reference)。

模型目录不是兼容性证明：需要区分 Responses、Chat Completions、Anthropic Messages，以及 streaming、工具调用、分组授权和客户端限制。模型名称不作为唯一判断依据；无法确认时显示“待验证”，不静默选择其他协议。

## 网关授权流程

1. 首次打开输入账号密码，网关固定为 `https://surplustoken.com`。
2. Connect 直接请求 `POST /api/v1/auth/login`，获取用户 access token；如需二次验证，按接口结果处理。
3. 使用用户 Bearer token 查询用户信息、Key 列表与可用分组，复用合适的 Key；没有可用 Key 时按用户选择创建专用 Key。
4. 使用推理 API Key 获取模型目录并检查权限，再选择客户端、模型与配置模式。
5. 展示脱敏变更，应用永久配置或构造临时启动方案。

首版不需要浏览器回调、loopback 服务或网关新增 Web Import 页面。账号密码仅用于登录，不保存到配置文件或日志；access token 仅在内存中使用。桌面版将 refresh token 存入操作系统凭据库，打开时自动交换新令牌，刷新轮换同步保存。过期或撤销后回到登录；暂时断网保留会话并允许重试。退出登录清除保存状态。

面板用户 token 与推理 API Key 分开管理，前者不能注入模型客户端，也不需要管理员 Key。若已有 Key 无法再次取回完整值，应创建专用 Key 或让用户手动导入。

2026-10-13 已用测试账号验证登录、用户资料、Key 列表和五个可用分组，并经用户授权创建一个名为“SurplusToken Connect”的 Key。真实 `/v1/models` 初次返回余额不足，随后在原生应用中成功读取并显示 gpt-5.5；未发起推理请求。原生应用已验证完全退出再打开自动恢复登录，以及主动退出后重开回到登录页。二次验证通过接口夹具测试，尚未生产联调。密码、令牌和 Key 明文不写入仓库。

## 配置目录与隔离

临时模式可以使用 Connect 专属目录，但目录覆盖不等于完整隔离：项目配置、系统策略、操作系统凭据库仍可能参与。优先仅为子进程设置对应变量，不修改系统级环境或用户 shell 配置。

| 客户端 | 目录/配置变量 | 作用与边界 |
| --- | --- | --- |
| Codex | `CODEX_HOME` | 默认 `~/.codex`，覆盖配置、文件认证与会话等状态根目录；指定目录需预先存在。系统凭据存储与 Desktop 行为另行验证 |
| Claude Code | `CLAUDE_CONFIG_DIR` | 将用户配置、会话和插件迁移到指定目录；不自动排除项目或受管配置 |
| Kimi Code | `KIMI_CODE_HOME` | 默认 `~/.kimi-code`；配置位于该目录的 `config.toml` |
| oh-my-pi | `PI_CODING_AGENT_DIR` | 默认 `~/.omp/agent`；原生 agent 数据目录覆盖，但通用配置发现不是全部跟随此变量；命名 profile 会忽略它 |
| OpenCode v2 | `OPENCODE_CONFIG_DIR`、XDG 目录变量 | v2 已核对目录覆盖与多层合并；账户数据与配置分开，不能只靠配置目录变量隔离。v1 的文件/内联覆盖变量未当作 v2 接口使用 |

来源与详细边界见 [调研文档](docs/prior-work.md#客户端目录变量与隔离边界)。适配器应分别记录配置目录、凭据位置、会话目录和项目配置来源；用户未选择隔离目录时，应尊重已有环境变量，不硬编码默认路径。永久模式写入用户实际使用的配置位置，避免写进只有 Connect 启动时才读取的目录。

**当前开发约束：不启动、重启或测试运行 Codex CLI / Codex Desktop，也不改写当前使用中的 Codex 配置与凭据。** Codex 适配阶段只做文档/源码核查和临时目录中的静态夹具验证，实际运行测试待用户明确解除限制后再进行。

## 技术架构

采用 Go 负责核心逻辑，Wails v3 提供桌面窗口与 Go/前端桥接；前端采用 React + TypeScript + Vite，组件使用 Kumo 2.14.0。原生 GUI 与开发 HTTP API 共享 Go 应用服务；面向用户的 CLI 和 Launcher 尚未实现。

```text
Wails v3 GUI / 开发 HTTP API
        │
        ▼
Go application services
  ├── Gateway：授权、Key、分组、模型目录
  ├── Credentials：操作系统凭据库
  ├── Discovery：可执行文件、版本、配置目录
  ├── Adapters：各客户端协议、配置和启动方式
  ├── Config：预览、备份、合并、应用、恢复
  └── Launcher（规划）：进程环境、终端启动、退出清理

AI 编程客户端 ── 直接请求 ── SurplusToken / Sub2API
```

当前代码组织：

```text
main.go                 Wails 桌面入口与 Go Service 注册
cmd/connect-dev/        开发 HTTP API 与离线夹具入口
frontend/               React / Wails 前端
internal/gateway/       Sub2API 登录、令牌与 Key API 集成
internal/httpapi/       请求处理、原生桥接与客户端配置适配
internal/sessionstore/  系统凭据库
internal/discovery/     客户端发现与版本检测
internal/configedit/    JSON/JSONC 局部编辑
internal/configuration/ 配置规划、备份、应用与恢复
docs/                   调研、设计与实现记录
```

适配器规划覆盖探测、配置规划、应用、恢复和临时启动；当前已开放的永久配置能力见下方开发路线。只生成启动方案的逻辑不依赖 Wails、终端交互或磁盘写入，以便单独验证。

### 配置与凭据原则

- Connect 自身的秘密优先存入 macOS Keychain / Windows Credential Manager，日志与前端状态默认脱敏。
- 保留未知字段、用户自定义 provider、MCP 和其他无关设置；不能安全合并时停止写入并解释冲突。
- 原文件备份后进行最小修改；写入前校验快照未变化，再执行单文件原子替换。多文件操作记录逐项结果，失败时可恢复，不声称跨文件天然原子。
- 恢复时检查文件是否又被用户或客户端修改，避免旧备份覆盖新设置。备份与原始凭据采用相同等级的访问限制。
- API Key 不放入命令行参数、日志或 Git；子进程只获得运行所需的凭据。
- GUI 启动交互式 CLI 需要终端承载与正确的退出/信号处理，首版优先使用外部终端，内嵌终端另行评估。

## 开发路线

1. **已实现**：Wails v3 外壳、直接 API 登录、分组与 Key 复用/创建、模型列表接口、客户端发现。
2. **首批永久配置**：Claude Code、OpenCode v2 的变更预览、受限备份、最小合并、恢复与冲突检测已通过隔离目录联调；真实推理待验收。
3. **临时启动与适配扩展**：补齐终端承载、oh-my-pi、Kimi Code、其他客户端的两种模式。
4. **Codex Desktop**：继续静态适配研究；运行验证须等用户解除当前保护限制。
5. **发布**：Windows/macOS 安装包、签名与公证流程、升级策略，以及明确的客户端版本兼容表。

适配器验收至少覆盖：不含秘密的配置夹具、未知字段保留、重复应用幂等、写入失败恢复、外部修改冲突、临时启动不改全局配置，以及目标操作系统上的实际客户端请求。静态配置检查不能替代桌面端联调。

## 开发准备与参考源码

已验证 Go 1.27.2、Node.js 24.12.0（macOS）/24.19.0（Windows）；当前采用 **Wails v3**，Go 模块锁定 `v3.0.0-alpha.63`（`go.mod`），前端 `@wailsio/runtime` 锁定 `3.0.0-beta.28`（`frontend/package-lock.json`）；两者版本号不同，升级时需核对桥接 API 并验证原生调用，前端为 React + TypeScript + Vite。本工程 macOS 编译部署目标为 13.0（不代表已验证最低系统上的 WebKit / Kumo 兼容性），需要 Xcode 开发工具。Windows amd64 已在无 C 编译器的环境中完成普通测试与生产构建，运行需要 WebView2；启用 Go race 测试另需兼容 C 工具链。

```sh
# 安装依赖
npm --prefix frontend ci
go mod download

# 开发 API（一个终端，仅监听本机 43129）
make api-dev

# 前端热更新（另一个终端；/connect 请求经 Vite 转发到上述服务）
npm --prefix frontend run dev

# 编译桌面应用
npm --prefix frontend run build
make build # macOS：包含一致的系统部署目标参数

# macOS / Linux 启动 Connect 自身
./bin/surplustoken-connect

# macOS 本地 .app 包（未签名、公证）
make package-mac
```

macOS 也可使用 `make run` 一步构建并启动。Windows 在 Go、Node.js/npm 可从 PATH 找到后使用 PowerShell：

```powershell
# Go 测试、vet、前端回归与生产构建；缺少 C 编译器时明确提示未运行 race
.\scripts\test-windows.ps1
# 构建无额外控制台的桌面 exe；已有前端产物时可加 -SkipFrontend
.\scripts\build-windows.ps1
# 正常运行
.\bin\surplustoken-connect.exe
# 隔离目录检查：禁用系统登录存储和真实客户端检测，WebView2 数据也放在此目录
.\bin\surplustoken-connect.exe --fixture-dir .\tmp\native-fixture
# 离线网页联调：另一个终端运行 npm --prefix frontend run dev
go run ./cmd/connect-dev --fixture-dir .\tmp\ui-fixture --mock
```

原生 `--fixture-dir` 隔离本机状态，但登录仍访问真实网关；完全离线测试请使用开发服务的 `--mock`。独立 Windows Credential Manager 集成测试可设置 `CONNECT_TEST_CREDENTIAL_STORE=1` 后运行 `go test ./internal/sessionstore`，仅创建并删除随机命名的测试条目。CI 已配置 Windows/macOS 的 race、vet、前端测试与桌面编译；托管 runner 的执行结果见仓库的 GitHub Actions，本地测试结果不替代跨平台 CI 验证。

当前无需全局安装 Wails CLI，尚未配置 Go 热重载和发行安装器。输出 exe 未签名。界面源码在 `frontend/src/`，桌面入口为 `main.go`。`build/` 平台资源纳入版本控制，`bin/` 和前端产物忽略。

启动自动恢复登录；首次使用或会话过期时显示独立登录页。过期后仍可显式清除保存的登录，清除失败保留重试入口。密码不保存，access token 仅在 Go 内存，refresh token 存入系统凭据库。账号昵称来自用户资料，左下角头像打开账号弹层。默认窗口 880 × 640，最小 820 × 600。Key 仅在 Go 内存与用户确认写入的客户端文件/备份中存在，前端只获得掩码。永久配置的备份可能含原有凭据，按私密文件保存。macOS 使用 Security.framework 钥匙串，Windows Credential Manager 已通过独立条目的保存、轮换与删除测试；含秘密文件及备份目录使用限制为当前用户和 SYSTEM 的 Windows DACL。网页预览仅记住本地开发会话 ID（8 小时、服务重启失效）和账号名，不保存密码或网关令牌。

开发 API 只用于 UI 和配置操作，不转发模型推理；桌面包使用原生 Wails Service，复用同一套 API 与配置逻辑，无需运行外部开发服务。测试模式必须显式指定目录：

```sh
# 真实网关 + 测试配置目录
 go run ./cmd/connect-dev --fixture-dir /private/tmp/connect-fixture
# 离线接口夹具 + 测试目录（不访问真实网关）
 go run ./cmd/connect-dev --fixture-dir /private/tmp/connect-fixture --mock
# 配置安全测试和前端构建
 make test
```

两种测试模式都在主页明确标记“测试目录模式”，客户端检测使用固定的未安装夹具，不执行本机版本探测。Key 按客户端独立选择，选择本身不写配置，须预览并应用。OpenCode 切换 Key/协议会清理 Connect 先前管理的失效字段，保留用户原有字段、后来追加的属性及注释；用户修改过受管字段时停止并报告冲突。真实操作前请核对变更预览中的目标路径。本轮实现范围、接口契约及验证记录见 [API 与配置接入](docs/api-configuration-implementation.md)。

### 离线界面回归

Go 与 Node.js/npm 可从 PATH 找到后，执行：

```sh
npm --prefix frontend ci
# 首次使用安装 Playwright Chromium；测试运行本身仅访问本机
npm --prefix frontend run test:e2e:install
npm --prefix frontend run test:e2e
```

Windows 已安装 Edge 时，也可在 PowerShell 设置 `$env:CONNECT_E2E_CHANNEL='msedge'`，使用独立无头浏览器运行测试，省去 Chromium 下载。测试不会使用日常浏览器个人资料。

`frontend/e2e/` 每条用例自动创建独立的临时配置目录、启动离线网关夹具与开发服务，并在结束时关闭服务及删除临时目录。前端独占 `127.0.0.1:5173`，端口被占用时失败，不复用已有服务；后端使用随机回环端口。覆盖按客户端选择 Key、创建与刷新 Key、Claude/OpenCode 写入与恢复、协议切换、模型重试、写入失败后重新预览，以及过期登录与过时响应。失败截图、trace 和 JSON 结果保存在被 Git 忽略的 `tmp/e2e-results/`。

手动启动离线 API 时，`--listen 127.0.0.1:0` 可自动分配端口；启动完成后输出 `CONNECT_DEV_READY` 和包含实际地址、测试目录、离线模式的 JSON。自定义端口只用于开发服务，原生桌面入口仍走 Wails 桥接。

tf-cli 已作为本地临时参考克隆到 `.references/tf-cli/`，整个 `.references/` 被 Git 忽略，不是子模块或项目依赖。首次克隆可用：

```sh
git clone --depth 1 https://github.com/TokenFlux/tf-cli.git .references/tf-cli
```

本次参考提交：`d32cefdda83201aa92655083637b3e27e20cbf1d`（2026-10-13 查阅）。详细项目对比、源码入口和组件清单见 [Prior work 与可复用组件](docs/prior-work.md)。

## 许可

本项目尚未确定许可证。当前界面采用 Kumo、Phosphor Icons 与 Lobe Icons，来源与许可见 [第三方声明](THIRD_PARTY_NOTICES.md)。tf-cli 为 Apache-2.0，CC Switch 为 MIT；后续采用代码时记录来源、版本和修改，并保留适用的许可与版权声明。其他候选项目的许可与复用边界见调研文档。

Wails 外壳审计、已修复项与发行前缺项见 [Wails 排查报告](docs/wails-audit.md)。

配置与适配的设计方案见 [配置与恢复流程](docs/configuration-workflows.md) 和 [Codex / Desktop 适配](docs/codex-desktop-adaptation.md)。

# AGENTS.md

本仓库是基于 MetaCubeX/mihomo 的 Go 网络代理内核 fork。功能概览和用户配置见 [README.md](README.md)。本文件给出构建、验证和修改代码时的主要入口。

## 构建与配置校验

```sh
CGO_ENABLED=0 go build -tags with_gvisor
./mihomo -t -d /path/to/test-home -f /path/to/config.yaml

make darwin-arm64
make linux-amd64-v3
make all
```

- `with_gvisor` 启用 gVisor 相关实现，也用于编入 Tailscale 出站。设置 `no_tailscale` 会排除 Tailscale 实现。
- [Makefile](Makefile) 的平台目标把带版本和构建时间的二进制写到 `bin/`。`make all` 构建 Linux、macOS 和 Windows 的 amd64-v3、arm64 六个目标，`make all-arch` 才遍历全部列出的目标。
- fork 的正式发布使用 [sync-and-build.yml](.github/workflows/sync-and-build.yml) 指定的 Go 工具链和构建矩阵。Linux、macOS、Windows 发布使用 `CGO_ENABLED=0`，Android 发布使用 NDK 和 `CGO_ENABLED=1`。内嵌 Android 客户端还需关注 `android && cmfa` 路径。
- `-t` 校验当前设备展开后的配置。验证端口绑定、TLS 握手或重载行为时，另外用临时配置和回环地址运行内核。

## 测试与代码检查

```sh
CGO_ENABLED=0 go test -count=1 -run '^TestDeviceOverridesLoad$' ./config
CGO_ENABLED=0 SKIP_INTEROP_TEST=1 go test -count=1 ./...
CGO_ENABLED=0 SKIP_INTEROP_TEST=1 go test -tags with_gvisor -count=1 ./...
CGO_ENABLED=1 go test -race -tags with_gvisor -count=1 ./hub/...
make lint
```

- 优先运行与改动相关的用例和包。需要对照 CI 时，查 [test.yml](.github/workflows/test.yml) 的平台和标签矩阵，以及 [fork 发布流程](.github/workflows/sync-and-build.yml) 的定向测试。测试矩阵会在 macOS 跳过 `listener/inbound` 的测试文件。本地排查时可用 `go test -run` 单独运行该包的目标用例。
- Linux 透明 UDP 监听用例依赖相应的 socket 权限。需要验证权限降级时，使用无相关能力的 Linux 进程，并核对用例实际执行。交叉构建只验证编译。
- [test/](test/README.md) 是独立 Go 模块，协议集成测试在 Docker 可用的环境执行 `make -C test test`。
- 检查器由 [.golangci.yaml](.golangci.yaml) 配置，包括 `gofumpt`、`staticcheck`、`govet`、`gci`。导入顺序为标准库、`github.com/metacubex/mihomo`、第三方库。常用别名为 `constant` → `C`、`common/net` → `N`、`constant/provider` → `P`。

## 代码入口

| 目录 | 职责 |
| --- | --- |
| `config/` | YAML 默认值、解码和校验，生成运行时配置 |
| `hub/executor/`、`hub/route/` | 应用配置、初始化 provider、协调控制器和 REST 配置重载 |
| `listener/`、`adapter/inbound/` | 套接字绑定、入站协议处理和连接元数据 |
| `tunnel/`、`rules/` | 接收 TCP/UDP 流量、规则匹配和连接转发 |
| `adapter/outbound/`、`adapter/outboundgroup/` | 出站协议、代理组选择和拨号 |
| `adapter/provider/`、`rules/provider/`、`component/resource/` | 代理订阅、规则集和资源下载 |
| `dns/`、`component/resolver/`、`component/fakeip/` | DNS 服务、解析器选择、缓存和 Fake-IP 映射 |
| `constant/`、`transport/` | 共用接口和底层协议实现 |

- 配置加载分为 `config.UnmarshalRawConfig` 和 `config.ParseRawConfig`。设备覆写在原始 YAML 解码阶段生效。内嵌客户端在系统 VPN 及应用配置补丁之前调用 `config.ExpandDeviceOverrides`。
- 独立进程启动与 `SIGHUP` 通过 `hub.Parse` 进入 `route.ParseConfig`，经 executor 读取和解析配置，再由 `forkApplyConfig` 协调 `executor.ApplyConfigWithListeners` 和控制器。内嵌客户端传入已解析配置时走 `hub.ApplyConfig`。
- REST `PUT /configs` 重新解析配置。切入、切出和重载设备配置会更新代理、DNS 与控制器监听。普通配置沿用 `force` 语义。`PATCH /configs` 只改运行时设置。处理监听改动时，参考 `hub/fork_device_reload_test.go` 和 [设备覆写示例](docs/device-overrides.yaml)。
- 流量从监听器进入 `tunnel.HandleTCPConn` 或 `tunnel.HandleUDPPacket`，再经规则和代理组选择出站。独立程序入口对 `net.DefaultResolver` 的默认拨号设置了进程退出守卫。需要解析域名时沿用 `component/resolver` 的相应解析路径。

## Fork 功能入口

- `policy-priority` 和 `select.default` 的匹配逻辑及测试位于 `adapter/outboundgroup/`。配置字段还要检查 `config/` 和 [docs/config.yaml](docs/config.yaml)。
- `subnet` 涉及 `component/netenv/` 的网络快照、`adapter/outboundgroup/fork_subnet.go` 的出站选择、`hub/executor/fork_subnet.go` 的连接回收及 DNS 缓存失效。验证切网时检查实际出站与 DNS 路径。配置、`/network` 接口及平台限制见 [subnet 文档](docs/fork_subnet.md)。
- Tailscale 的移动网络通知和控制节点健康状态在 `adapter/outbound/tailscale*.go`。随网络变化、重载或切换运行模式的改动，应核对内嵌入口以及独立内核入口。
- [发布工作流](.github/workflows/sync-and-build.yml) 会把 `Meta` 的 fork 提交重放到上游稳定版标签后再构建。修改上游共用入口时，把 fork 特有逻辑尽量留在对应的 fork 模块，方便后续跟进上游。

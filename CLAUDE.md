# CLAUDE.md

本文件为 Claude Code (claude.ai/code) 在此仓库中工作时提供指导。

## 项目概述

Mihomo（Meta Kernel）是一个用 Go 编写的高性能网络代理服务器，支持多种协议（VMess、VLESS、Shadowsocks、Trojan、Snell、TUIC、Hysteria、WireGuard 等），具备基于规则的流量路由、内置 DNS 和 RESTful API 控制器。属于 MetaCubeX 生态系统，从 Clash 分支而来。

## 构建命令

```bash
# 快速构建（当前平台）
go build

# 启用 gVisor TUN 协议栈构建（Makefile 默认使用）
go build -tags with_gvisor

# 完整生产构建（静态链接、去除符号表、附带版本信息）
CGO_ENABLED=0 go build -tags with_gvisor -trimpath -ldflags '-w -s'

# 通过 Makefile 交叉编译（例如 darwin-arm64、linux-amd64-v3）
make darwin-arm64
make linux-amd64-v3

# 构建所有默认平台
make all
```

## 测试与代码检查

```bash
# 运行所有测试
go test ./... -v -count=1

# 运行单个测试
go test -v -run TestFunctionName ./path/to/package

# 协议集成测试（在 test/ 目录下，顺序执行）
cd test && go test -p 1 -v ./...

# 代码检查（需要 golangci-lint）
golangci-lint run ./...
```

## 代码检查规则

启用的检查器：**gofumpt**、**staticcheck**、**govet**、**gci**。

导入排序（由 gci 强制执行）：
1. 标准库
2. `github.com/metacubex/mihomo`（项目内部）
3. 第三方库

## 架构

### 连接流程

```
入站连接 → listener/* → tunnel.HandleTCPConn/HandleUDPPacket
  → 规则匹配 → 代理组选择 → adapter/outbound/* → 远端服务器
```

### 核心包

| 包 | 职责 |
|---|------|
| `constant/` | 核心接口（`ProxyAdapter`、`Conn`、`Rule`、`Proxy`）和类型枚举 |
| `config/` | YAML 配置文件解析 |
| `adapter/inbound/` | 入站连接适配器（HTTP、SOCKS5、Vless 等） |
| `adapter/outbound/` | 出站协议实现（27+ 种协议） |
| `adapter/outboundgroup/` | 代理组策略：Selector、Fallback、URLTest、LoadBalance |
| `adapter/provider/` | 动态代理/规则提供者管理 |
| `listener/` | 协议特定的服务监听器 |
| `transport/` | 底层协议传输实现 |
| `tunnel/` | 核心连接路由及 TCP/UDP 队列管理 |
| `rules/` | 路由规则引擎（域名、GEOIP、IPCIDR、进程、逻辑运算符） |
| `dns/` | DNS 服务器，支持 DoH/DoT 和 Fake IP |
| `component/` | 共享组件：拨号器、解析器、进程检测、fakeip、地理数据 |
| `hub/executor/` | 配置应用——将监听器、代理、规则、DNS 组装在一起 |
| `hub/route/` | RESTful API 控制器（使用 chi 路由器） |

### 启动路径

`main.go` → `hub.Parse()` → `executor.Parse()` → `config.Parse()`（YAML）→ `executor.ApplyConfig()`（应用到所有子系统）→ `route.ReCreateServer()`（API）

收到 SIGHUP 信号后的配置重载遵循相同路径：`hub.Parse()` → `ApplyConfig()`。

### 代码约定

- `constant` 包按惯例导入为 `C`：`C "github.com/metacubex/mihomo/constant"`
- `common/net` 导入为 `N`：`N "github.com/metacubex/mihomo/common/net"`
- `constant/provider` 导入为 `P`：`P "github.com/metacubex/mihomo/constant/provider"`
- 构建标签 `with_gvisor` 用于启用基于 gVisor 的 TUN 协议栈
- 所有构建均禁用 CGO（`CGO_ENABLED=0`）
- `net.DefaultResolver` 在 main.go 中被故意"毒化"——所有 DNS 请求必须通过 mihomo 的解析器

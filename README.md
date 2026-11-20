# Mihomo Kernel (个人自用版)

基于上游 Mihomo 的个人自用版本。以下列出本 fork 新增或扩展的功能。

## 1. `url-test` 组支持策略优先级 (`policy-priority`)

在 `url-test` 类型的代理组中新增 `policy-priority` 配置项,允许为匹配特定模式的节点设置延迟权重(倍率)。
计算"最优节点"时会用 `实际延迟 × 权重` 来排序,权重小于 1 的节点更易胜出,大于 1 则会被劣后。
这样可以在不牺牲自动测速能力的前提下,优先选择某些线路(如自建/低费率节点),仅在它们明显劣于其他节点时才切换。

示例:

```yaml
proxy-groups:
  - name: 🇭🇰 HK
    type: url-test
    include-all-providers: true
    url: https://www.gstatic.com/generate_204
    interval: 300
    policy-priority: "Premium:0.8;Hong Kong:0.85"
```

## 2. `select` 组的默认节点匹配（`default`）

`default` 优先于上游的 `default-selected`。先查找精确名称，找不到时，有 `*` 或 `?` 就按通配符匹配，其余按前缀匹配。命中多个候选时先用第一个，再在后台探测可用性，期间仍可读取当前选择和拨号。

示例:

```yaml
proxy-groups:
  - name: 🇭🇰 HK
    type: select
    include-all-providers: true
    filter: "🇭🇰|JMS"
    default: "JMS-*"
```

## 3. Tailscale 出站增强

在上游的 `type: tailscale` 出站上增加了两项能力。

- 移动端前端可以上报 Wi-Fi、蜂窝网络等底层网络变化，唤醒内嵌 Tailscale 节点重新检测网络。接入入口是 `outbound.NotifyTailscaleNetworkChange`。
- 当控制面返回 `PollNetMap 404 node not found` 时，将该出站标记为不可用，并在日志中提示重新注册节点。

示例:

```yaml
proxies:
  - name: ts-mihomo
    type: tailscale
    auth-key: tskey-auth-xxxxx
    hostname: ts-mihomo
    control-url: https://controlplane.tailscale.com
    ephemeral: true # 是否是临时节点
    state-dir: "./tailscale"  # 连接上tailscale网络之后会有一些数据持久化,该选项是就指定保存这些数据的目录
    exit-node: "" # 可选，不需要出口节点就不填
    accept-routes: true # 可选，默认 true，用于接受 subnet routes
    udp: true # 需要转发 UDP 时开启

rules:
  # 将你家里的内网流量打到tailscale中
  - IP-CIDR,192.168.1.0/24,ts-mihomo,no-resolve
```

## 4. 按当前网络选择出站（`type: subnet`）

`subnet` 按当前物理网络依次匹配 `matches`，支持网络类型、SSID、BSSID、DHCP 服务器、网关地址、网关 MAC 和接口名。同一条内的条件须全部满足。已知网络无匹配时使用 `default`。刚启动还没有网络快照时暂用第一条，默认 10 秒后仍未知则转为 `default`。

规则和 DNS 策略可以共用同一个组。比如在家直连 NAS 和家里的 DNS，离家后把两者都送入上例的 `ts-mihomo`。

```yaml
proxy-groups:
  - name: Home
    type: subnet
    default: ts-mihomo
    matches:
      - {ssid: MyHomeWiFi, proxy: DIRECT}

dns:
  enable: true
  nameserver-policy:
    "+.home.arpa": "192.168.1.1#Home"

rules:
  - DOMAIN-SUFFIX,home.arpa,Home
  - IP-CIDR,192.168.1.0/24,Home,no-resolve
```

出站切换时，内核关闭经过该组的连接并刷新 DNS 缓存。桌面端可自动检测底层网络，移动端依赖前端上报。`GET /network` 可查看当前网络、字段来源及代理组命中情况。[完整配置、控制器 API 和平台限制](docs/fork_subnet.md)

## 5. 同一份配置按设备生效（`device-overrides`）

顶层 `device-overrides` 可按 `os` 和 `device-name` 匹配设备。所有命中项按文件顺序应用，`set` 递归合并映射并替换标量和列表，`prepend.rules` 把规则按顺序插到公共规则前。没有命中时使用公共配置。

```yaml
rules:
  - MATCH,REJECT

device-overrides:
  - when: {os: darwin, device-name: "Example Mac mini"}
    prepend:
      rules:
        - DOMAIN,mac-only.example,DIRECT
```

可用 `-device-name` 或 `MIHOMO_DEVICE_NAME` 指定设备名。通过 `SIGHUP` 或 REST `PUT /configs` 重载时，会重新匹配设备并更新代理、DNS 和控制器监听。Android CMFA 前端需要在准备 VPN 前调用 `config.ExpandDeviceOverrides`。未接入该接口的客户端会继续使用公共配置。[按设备开放局域网监听的完整示例](docs/device-overrides.yaml)

## 6. Provider 下载遵循指定代理

HTTP 类型的代理订阅和规则集配置了 `proxy` 时，指定代理拨号失败会直接报错，避免下载请求回退到直连。下载日志会记录目标主机、代理名和 HTTP 状态。

## 7. 内嵌客户端集成

内嵌模式下可以通过 `PATCH /configs` 单独切换 `mode`，例如发送 `{"mode":"rule"}`。宿主应用可通过 `executor.SetApplyConfigHook` 在内核应用配置后同步运行状态。

---

## 下载、更新与构建

本 fork 的内核更新接口从本仓库的最新 Release 下载，兼容带版本号的压缩包文件名。发布流程会在上游稳定版的基础上构建 fork 版本，并随压缩包发布 `checksums.txt`。[本 fork 的 Releases](https://github.com/cyenxchen/mihomo/releases)

```shell
git clone --branch Meta https://github.com/cyenxchen/mihomo.git
cd mihomo && go mod download
CGO_ENABLED=0 go build -tags with_gvisor
```

更多文档参考 [mihomo Wiki](https://wiki.metacubex.one/)。

## License

GPL-3.0

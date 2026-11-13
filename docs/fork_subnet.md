# 按网络环境选择出站（fork）

`subnet` 按当前物理默认网络匹配出站，不进行健康检查，不因出站故障改选，不支持手动选择。

```yaml
proxies:
  - name: ts-mihomo
    type: tailscale
    # 其余 Tailscale 参数沿用已有配置。
    udp: true
proxy-groups:
  - name: Corp
    type: subnet
    unknown-timeout: 10 # 可省略；必须是正整数秒
    default: ts-mihomo
    matches:
      - {ssid: GOLDWIND, proxy: DIRECT}
      - {dhcp-server: 10.15.200.1, proxy: DIRECT}
      - {type: cellular, proxy: ts-mihomo}
dns:
  enable: true
  nameserver-policy:
    "+.goldwind.com": "10.15.200.1#Corp"
rules:
  - DOMAIN-SUFFIX,goldwind.com,Corp
  - IP-CIDR,10.0.0.0/8,Corp,no-resolve
```

以上片段合并到已有配置；示例 DNS 地址必须替换为实际公司 DNS。保留其他域名策略、普通代理规则与最终 REJECT 规则。`ts-mihomo` 须是顶层出站，不能只存在于 provider 中。Headscale 控制面与 DERP 域名应在公司域名及内网网段之外，避免解析/连接回环。

## 匹配与状态

每项至少有一个条件及 `proxy`；按顺序取第一项全部满足的规则，均不匹配时取精确名称 `default`。条件支持 `type`（`wifi`、`wired`、`cellular`）、`ssid`、`bssid`、`dhcp-server`、`gateway`、`gateway-mac`、`interface`，均为单个字符串。MAC/IP 会规范化；SSID 与接口名区分大小写。不接受 `proxies`、`use`、`include-all*`、`filter` 或 `exclude-*` 成员过滤配置。

- `unknown` 仅指进程从未收到任何有效快照：首次应用该组后的 `unknown-timeout` 秒内选择 **matches 第一项**（示例 DIRECT），到期仍未知则主动改选 **default**（示例 ts-mihomo），执行完整切换副作用并告警一次。缺省 10 秒，只接受正整数。同名组重载或删除后重新加入不重置起点，新组名从首次应用起计时；更改时限沿用原起点。一旦收到有效快照便取消计时。
- `none` 是已知断网，选择 `default`。
- 曾收到快照后，自检失败/超时、停止内核或清除唯一上报来源均保留最后已知结果。启动前上报失败时，也可能暂时沿用旧网络的选择。
- 字段读不到仅使依赖该字段的条目不匹配；macOS 命令行通常读不到 SSID/BSSID，可用 DHCP 服务器等条件。
- 自检顺序为配置 `interface-name`、TUN 网卡查找器、排除隧道的物理默认路由；事件去抖 1 秒，轮询 10 秒，首次等待最多 2 秒，单次取数超时 5 秒。自检只在存在 subnet 组时运行，Android/iOS 不启动自检。
- 上报按字段覆盖自检；桌面上报绑定收到时的物理网络身份，身份改变后失效。不存在有效来源时保留最后已知结果。

## 切换、DNS 与 UDP

选择变化时同步更新选择、关闭链路含该组的 TCP/UDP 连接（包括 DNS）、推进 DNS 缓存代次并清空、重置 DNS 连接。fake-ip 映射保留。未改变选择时不执行这些副作用。

L3 出站的域名先按主解析器策略解析，解析失败就失败，不回退公网 DNS。UDP 首包及同一会话后续域名包都遵循此策略。拨号途中选择改变时，新连接会关闭并报错。

`SupportUDP` 在 API 中恒为 true，以防 UDP 规则被跳过；组 `disable-udp` 或当前成员不支持 UDP 时，发包返回错误。Tailscale 默认未开启 UDP，因此需要 **`udp: true`**。单纯看到 API 的 `udp: true` 不代表底层支持 UDP。

缓存失效前已经发出的查询仍可把旧结果返回给原调用方，但不能写回缓存，也不会被失效后发起的新查询共享。`dialer-proxy` 间接链路的追踪不在本次范围内；TUN 的 ICMP 不经过规则，不要用 ping 验证路径。

## 上报与排障

内嵌前端使用 `component/netenv.Default.Report(ctx, fields, "embed")`、`ClearReport()`、`Current()`/`View()`、`Subscribe()`。启动前可以上报；Stop/Start 不清除上报。

控制器接口：

- `GET /network`：状态、合并字段、字段来源、缺失字段、原始槽位/时间/有效性、快照代次与各组当前出站/命中条目；每组的 `unknown-timeout` 为秒数，`unknown-timed-out` 表示是否因未知超时而选择 default。
- `PUT /network`：例如 `{"type":"wifi","interface":"en0","ssid":"GOLDWIND"}`。整体替换上报槽；省略或 null 的字段不覆盖自检。`{"type":"none"}` 表示已知无网络。成功 204，非法输入 400。
- `DELETE /network`：清除上报，成功 204；有自检则使用自检，否则保留最后结果。

HTTP 写入必须配置 secret 并按控制器规则鉴权；无 secret 的 HTTP 写入返回 403。unix socket/命名管道靠本地权限放行写入。embedMode 禁止所有 REST 写入，仍允许读取。SSID/BSSID/MAC 按批准设计在日志和接口中保留原值。

Android 在「应用设置 → 网络定位权限」申请定位：先前台精确定位，再单独申请/引导后台“始终允许”。拒绝权限不妨碍 VPN 运行；改用可读的 DHCP、网关、网络类型等标识。Android 11+ 后台权限通过应用系统设置授权，返回页面后刷新状态。前台申请仍被拒绝时显示前往应用设置的提示。定位开关和运行中授权变化会重新注册网络回调并上报；只接收本应用广播，外部授权/定位开关变化由 10 秒轮询补获。API 31+ 使用含定位信息的最佳网络回调，API 26–30 使用非 VPN 网络请求；Wi-Fi 与蜂窝同时在线时不按 validated 列表顺序猜测默认网卡。底层网络丢失延迟 1 秒确认，期间有新网络则取消断网上报；新网络等到能力与链路回调均到齐后才上报。

## 已知限制

- DNS 同步失效也清理各解析器的 bootstrap `defaultResolver` 缓存。policy 客户端长连接仍由“连接链路包含已切换组”时关闭来覆盖；不扩展上游 `ResetConnection` 对 policy 客户端的遍历。未进入该连接追踪链的间接连接不在覆盖范围内。
- 配置应用阶段会对新旧同名 subnet 对象一起重新判定，缩小重载窗口。但 `apply` 完成到 `updateProxies` 之间仍不是原子切换；期间再次发生网络变化时，旧对象不能保证同步更新。组改名时旧 DNS resolver 的 `#旧组名` 也可能短暂查不到。彻底消除需要扩大上游配置/代理发布接入，本次保留该边界。
- Linux 有 nmcli 时只使用当前设备 DHCP 信息或当前连接 UUID 对应租约，不扫描历史文件。其他租约必须能验证接口及本次 carrier 代次的新鲜度；无法读取上电时间时，以首次观察该 carrier 代次的时间作为保守下界，可能暂时读不到已有的合法租约。不会用无法证实归属的旧 DHCP 地址参与匹配。
- 桌面上报时内联自检失败会绑定已有有效 Detect；从未有有效 Detect 的上报，只允许 3 秒内的首次自检补绑定，过期即失效。

## 用户验收

每项记录 `GET /network`、`GET /proxies/Corp` 的 `now`、公司资源与公司 DNS 的出站链路日志。

1. 公司 Wi-Fi、其他 Wi-Fi、蜂窝、断网恢复，检查命中条目和路径。
2. Wi-Fi/蜂窝同时在线时确认底层默认 Wi-Fi；有/无定位权限、关闭定位开关分别检查。
3. 切网前建立公司长连接，切换后确认断开；下一次 DNS 查询使用新路径。
4. 公司外让 Tailscale 不可用，公司 TCP/UDP 与 DNS 应失败，不能走 DIRECT 或后续规则。
5. 分别设置 `udp: true`/false 验证 UDP 行为。
6. **插网线确认有线网 DHCP 服务器与网关**；DHCP 服务器不是 `10.15.200.1` 时补匹配条目。
7. Windows 实机验证 wlanapi 定位授权、网关/DHCP/MAC；Linux 验证当前网络管理器的租约与 iw/nmcli 来源；macOS GUI 授权与上报需另验。
8. Android 冷启动、Stop/Start、运行中授权/撤权，确认启动前上报顺序与最后已知结果保留。

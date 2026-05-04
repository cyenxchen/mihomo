# Meta Kernel (个人自用版)

个人自用版本,相比原版多了以下功能:

## 1. `url-test` 组支持策略优先级 (`policy-priority`)

在 `url-test` 类型的代理组中新增 `policy-priority` 配置项,允许为匹配特定模式的节点设置延迟权重(倍率)。
计算"最优节点"时会用 `实际延迟 × 权重` 来排序,权重小于 1 的节点更易胜出,大于 1 则会被劣后。
这样可以在不牺牲自动测速能力的前提下,优先选择某些线路(如自建/低费率节点),仅在它们明显劣于其他节点时才切换。

示例:

```yaml
proxy-groups:
  - name: Auto
    type: url-test
    proxies: [HK-01, HK-02, JP-01, US-01]
    url: https://www.gstatic.com/generate_204
    interval: 300
    policy-priority: "HK*:0.5;JP*:0.8;US*:1.5"
```

## 2. `select` 组的 `default` 支持通配符

`select` 类型代理组的 `default` 字段除了原来的精确匹配 / 前缀匹配,现在还支持通配符 (`*` 和 `?`)。
匹配优先级:精确名称 > 通配符 > 前缀。后台异步探测保持原有非阻塞行为,选择器读取/拨号不会被阻塞。

示例:

```yaml
proxy-groups:
  - name: Proxy
    type: select
    proxies: [DIRECT, HK-Auto, JP-01, JP-02, US-01]
    default: "JP-*"   # 启动时自动选中第一个匹配 JP- 前缀的节点
```

## 3. 新增 Tailscale 出站协议 (`type: tailscale`)

通过内嵌 `tsnet` 节点直接将流量送入 tailnet,无需在宿主机上安装/运行 Tailscale 客户端。

主要能力:

- 配置项支持 `auth-key`、`hostname`、`control-url`、`ephemeral`、`accept-routes`、`exit-node` 等
- 默认开启子网路由接受;移除 `exit-node` 配置时会自动清理过期 prefs
- 完整保留 Tailscale 的 DNS 解析(MagicDNS、split-DNS),TCP 与 UDP 目标均生效
- 将 `net.DefaultResolver` 的"逃生通道"严格限定在 Tailscale 操作内,进程级 DNS 守卫仍能拦住其他越权调用
- SOCKS、Shadowsocks、Mieru、sing 系列入站允许 UDP 域名回写,避免回包退化为 fake-IP

示例:

```yaml
proxies:
  - name: tailnet
    type: tailscale
    auth-key: tskey-auth-xxxxxxxx
    hostname: mihomo-node
    accept-routes: true
    # exit-node: 100.x.x.x   # 可选:作为 exit node 出网
```

> 注意:由于引入 Tailscale 依赖,Go 版本下限相应抬升。

---

## 构建

```shell
git clone https://github.com/MetaCubeX/mihomo.git
cd mihomo && go mod download
go build -tags with_gvisor
```

更多文档参考 [mihomo Wiki](https://wiki.metacubex.one/)。

## License

GPL-3.0

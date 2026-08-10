# AI Agent 临时 SSH Lease 设计

> 日期：2026-08-07（2026-08-10 更新：Access Token 自动审批）  
> 目标：允许 AI Agent 在最小权限、短生命周期、可撤销、可审计的前提下登录指定服务器。
>
> 审批模型：外部 AI 自助 API 一律以 **Access Token（`sct_*`）** 作为凭证；有效 Token 的申请自动批准并签发 Lease，Lease 有效期受 Token 有效期约束。不再提供人工审批与设备+节点免审批规则。

## 1. 安全结论

AI Agent **不得使用管理员提供的长期 SSH 密码**。推荐流程是：

1. AI 客户端本地生成一次性 SSH 密钥对。
2. 私钥始终保留在 AI 客户端临时存储中。
3. AI 只向 ServerCLI 上传公钥。
4. ServerCLI 创建 Lease 并让目标节点安装受约束的临时公钥。
5. Lease 到期、断开或撤销时，节点删除授权并终止对应会话（如果启用强制终止）。

这样即使数据库泄露，也不会得到可直接登录服务器的私钥或长期密码。

## 2. Lease 时间规则

默认配置：

```text
默认时长：1 小时
单次申请最大时长：1 小时（可配置但不得突破绝对上限）
自动续期：允许
绝对生命周期：首次签发后最多 24 小时
断连宽限：建议 60 秒
续期提前量：建议到期前 10 分钟
```

核心约束：

```text
lease_expires_at = min(申请时长, access_token.expires_at, absolute_expires_at)
new_expires_at   = min(now + requested_extension, access_token.expires_at, absolute_expires_at)
absolute_expires_at = issued_at + 24h
```

续期不得改变 `issued_at` 或 `absolute_expires_at`；到达 Token 到期或绝对上限后必须创建新申请。永久 Token 不突破 `absolute_expires_at`。

## 3. Lease 生命周期

```mermaid
stateDiagram-v2
    [*] --> Pending: AI 提交申请
    Pending --> Active: 自动/人工批准并安装公钥
    Pending --> Rejected: 策略或管理员拒绝
    Pending --> Failed: 节点离线/安装失败
    Active --> Active: 续期且未超过绝对上限
    Active --> Disconnected: AI 正常断开或连接守护确认结束
    Active --> Revoked: 管理员/策略撤销
    Active --> Expired: TTL 或 24h 上限到达
    Active --> Failed: 节点授权状态异常
    Disconnected --> [*]
    Revoked --> [*]
    Expired --> [*]
    Rejected --> [*]
    Failed --> [*]
```

## 4. 申请流程

```mermaid
sequenceDiagram
    participant AI as AI Agent
    participant CP as Control Plane
    participant NA as Node Agent
    participant SSH as OpenSSH

    AI->>AI: 生成一次性 ed25519 密钥对
    AI->>CP: 申请(Bearer sct_* Token, node, public_key, profile, 1h, purpose)
    CP->>CP: 校验全局/节点开关、策略、幂等键
    CP->>NA: 安装 lease_id 对应临时公钥
    NA->>SSH: 原子更新受管 authorized_keys
    NA-->>CP: 安装成功 + 指纹
    CP-->>AI: lease_id、主机、端口、到期时间
    AI->>SSH: 使用临时私钥登录
    SSH->>NA: forced wrapper 注册 session/cgroup
    NA->>CP: session_started
    loop 保活/续期
        AI->>CP: heartbeat / renew
        CP->>NA: 更新到期时间
    end
    AI->>CP: disconnect
    CP->>NA: 撤销并清理
    NA->>SSH: 删除公钥并终止/结束会话
```

## 5. 节点侧实现建议

### 5.1 专用账号

建议使用专用低权限账号，例如 `servercli-ai`，不要使用日常运维账号。该账号：

- 默认无密码；
- 仅允许公钥登录；
- 禁止端口转发、Agent 转发、X11 转发，除非某个权限配置明确需要；
- 默认无 sudo；
- 通过精确 sudoers wrapper 获得最小权限；
- home、authorized_keys 和审计目录由 root/Agent 管理。

### 5.2 临时 authorized_keys

每个 Lease 对应一条带约束的 key，例如概念形式：

```text
restrict,command="/usr/local/libexec/servercli-lease-shell --lease <lease_id> --token <lrt_运行时Token>" ssh-ed25519 AAAA... lease-<id>
```

具体 OpenSSH 版本兼容性需在目标系统验证。可显式添加：

- `no-agent-forwarding`
- `no-port-forwarding`
- `no-X11-forwarding`
- `no-user-rc`
- 是否允许 PTY 由权限配置决定

授权文件必须原子更新、校验所有者/权限，并只允许 Agent 的特权辅助进程写入。

### 5.3 会话包装器

`servercli-lease-shell` 负责：

1. 携带控制面签发的短生命周期**运行时 Token**（`X-Lease-Runtime-Token` 头）调用内部状态接口，验证 Lease 仍为 active、未到期、未禁用。运行时 Token 绑定 `lease_id + node_id + expires_at`，由控制面主密钥 HMAC 签名，仅能查询对应 Lease 状态；即使调度器尚未写入 `expired`，到达 `expires_at` 后也无法建立新 SSH 会话。
2. 创建 Lease 专属 cgroup 或记录进程树。
3. 记录会话开始、连接信息和请求命令。
4. 根据权限配置启动受限 shell、命令代理或审计终端。
5. 会话结束时上报退出状态并触发自动撤销。
6. 撤销时终止该 Lease 的进程组/cgroup。

若不实现会话进程跟踪，删除 authorized_keys 只能阻止新连接，**不能可靠终止已经建立的 SSH 会话**。因此“撤销即终止存量连接”必须作为单独验收项。

## 6. 自动申请与审批策略（Access Token）

外部 AI 自助 API（申请、查询、续期、心跳、断开）全部要求 `Authorization: Bearer <sct_* Access Token>`：

| Token 状态 | 行为 |
| --- | --- |
| 无 Token / 未识别 / 已撤销 / 已过期 | 一律返回 `401`，可识别 Token 写入使用日志 |
| 有效 Token | 申请校验通过后立即 `approved` 并签发 Lease（自动审批） |

- Token 只保存 SHA-256 哈希与前缀；明文仅在创建时返回一次。
- 固定有效期：`15m / 1h / 6h / 1d / 1w / never（永久）`。
- Lease 到期 = `min(申请时长, Token 到期时间, 系统绝对上限)`；永久 Token 不突破绝对上限。
- Token 到期或被撤销后：API 操作立即失败；关联活动 Lease 撤销；节点收到 `lease_keys_changed` 后删除公钥阻止新连接。
- Token 撤销不可恢复且幂等；撤销在同一业务事务内级联更新关联 Lease，事务成功后推送节点刷新。
- 每次可识别 Token 的请求写入 `api_token_usage_log`（方法、规范化路由、资源/操作、来源 IP、User-Agent、状态码、结果、关联申请/Lease、Token 当时状态）。
- 权限模型预留 `resource + action + constraints`（如 `permission_profiles:["read-only"]`）；首版所有 Token 为全权限 `*:*`。

审批顺序（不再有 manual/policy/disabled 模式与设备免审批规则）：

1. 全局/节点「允许新申请」开关（紧急控制）；
2. Token 有效性校验；
3. 授权检查（`Authorize(principal, resource, action, attrs)`）；
4. 参数与目标节点校验；
5. 自动批准并签发 Lease。

> 旧 `ai_approval_mode`、`ai_auto_approval` 数据保留用于历史追溯，但不再参与匹配；启动时一次性把遗留 `pending` 申请置为 `rejected`、把无 Token 的活动 Lease 置为 `revoked`。

## 7. 禁止凭证操作

UI 建议分开提供三个动作，避免语义模糊：

1. **暂停新申请**：拒绝新 Lease，不影响现有 Lease。
2. **暂停续期**：现有 Lease 可运行到当前到期时间，AI 无法延期。
3. **紧急撤销全部**：立即撤销目标范围内现有 Lease，并按配置终止会话。

支持作用范围：全局、单节点、单 AI Agent、单 Lease。

## 8. 断开自动失效

### 8.1 正常断开

- AI 客户端调用 `/disconnect`。
- SSH wrapper 检测会话结束并上报。
- Control Plane 把 Lease 标为 `disconnected`，Node Agent 删除公钥。
- 同一 Lease 不允许重新连接；需要新申请。

### 8.2 异常断开

使用多层兜底：

1. SSH 会话进程结束/网络 keepalive 超时，wrapper 上报结束。
2. AI 客户端 Lease heartbeat 中断，超过宽限期后撤销。
3. Lease 自身 TTL 到期后撤销。
4. 节点本地也独立检查到期时间，主节点离线时仍能移除过期授权。

注意：短暂网络抖动可能触发误撤销。建议 60 秒宽限且状态提示清晰；具体数值可配置。

## 9. 续期

续期需满足全部条件：

- Lease 当前为 active；
- 全局、节点、AI 和 Lease 均未禁止续期；
- AI 客户端使用绑定 Lease 的 Access Token（Token 未过期/未撤销且仍拥有 `ai.leases/renew` 权限）；
- 公钥指纹、目标节点和权限配置未改变；
- 未超过绝对 24 小时上限；
- 节点仍在线并确认本地授权状态一致；
- 风险策略未要求重新审批。

权限提升、目标节点变更或公钥变更都必须新建申请，不能通过续期完成。

## 10. 权限配置

权限档位与节点登录用户/提权方式一一对应（由 `servercli-lease-shell` 依据控制面 `/status` 返回的 `permission_profile` 执行）：

### `read-only`

- SSH 以 `servercli-ai` 普通用户登录，不执行任何提权。
- 允许查看系统信息和受限日志；不允许修改文件、重启服务、安装软件或网络访问扩展。

### `operator`

- SSH 以 `servercli-ai` 普通用户登录，不执行提权。
- 允许调用明确批准的维护 wrapper；正式环境可要求人工批准。

### `admin`

- SSH 仍以 `servercli-ai` 登录，但 `servercli-lease-shell` 检测到 `permission_profile=admin` 后经 `sudo -n` 提权到 root 执行（交互会话为 root 登录 shell）。
- 前置条件：节点需为 `servercli-ai` 配置 NOPASSWD sudoers（init 仓库 `restore_serverCli.sh` 自动写入 `/etc/sudoers.d/servercli-ai`）；未配置时 admin 会话报错退出，不会静默降权。
- 必须有可溯源的 Access Token 来源、极短时长、来源限制、会话录像、实时告警和强制终止能力缺一不可。

## 11. 审计范围

至少记录：

- 申请内容摘要、公钥指纹和幂等键；
- 自动/人工决策及原因；
- 安装/删除 authorized_keys 的结果；
- 续期请求、批准/拒绝和到期时间变化；
- Access Token 的创建/撤销与每次使用日志（方法、路由、状态码、结果、来源）；
- SSH 会话开始、结束、来源 IP、目标节点和退出状态；
- 通过 wrapper 执行的远程命令；
- sudo、文件变更和服务管理等关键事件；
- 管理员禁用、撤销、保护和删除记录的动作。

不得记录：私钥、密码、完整续期 Token、未脱敏 Secret 和无上限的终端原始内容。

## 12. 可观测性与告警

以下事件建议产生高优先级提示：

- 24 小时内连续大量申请或拒绝；
- 同一公钥指纹申请多个节点；
- 禁止续期后仍反复请求；
- 节点本地存在数据库未记录的临时公钥；
- Lease 已撤销但活动会话仍存在；
- 使用 `admin` profile；
- Agent 无法删除公钥或终止会话。

## 13. 验收用例

1. 默认申请返回 1 小时 Lease。
2. 相同幂等键重试不重复安装公钥。
3. AI 可使用临时私钥登录，长期密码未被使用或返回。
4. 到期前续期成功，但新的到期时间不超过首次签发 + 24 小时。
5. 禁止续期后续期请求被拒绝并审计。
6. 正常断开后 Lease 终止且同一私钥不能再次登录。
7. 异常断网超过宽限期后授权自动清除。
8. 管理员撤销后新连接失败；启用强制终止时现有会话也结束。
9. Node Agent 或 Control Plane 重启后不会恢复已过期/已撤销公钥。
10. 目标节点离线时申请失败或保持 pending，不产生“数据库 active、节点未安装”的假成功。
11. 超过 24 小时必须重新申请。
12. 重要申请/会话记录标记后不被每周清理删除。

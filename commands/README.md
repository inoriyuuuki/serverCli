# ServerCLI 命令样例（commands/）

本目录提供命令 manifest（YAML）与可执行文件样例，供 Agent 本地声明并在主节点远程调用。

## 目录约定

- `commands/<category>/<command-id>.yaml`：manifest（契约 §5 格式）
- `commands/<category>/<executable>`：可执行文件（bash，需 `+x`）
- manifest 中 `spec.executable` 为**相对 COMMANDS_DIR 的路径**（如 `./system/info`），
  Agent 会将其解析为 `<COMMANDS_DIR>/system/info` 并校验存在性/权限/哈希。

## 样例命令

| command_id            | 参数             | 说明                                   |
| --------------------- | ---------------- | -------------------------------------- |
| `system.info`         | 无               | 主机名 / 内核 / 运行时间（只读）       |
| `system.disk-usage`   | `mount` ∈ {/, /data}（必填） | df 输出                     |
| `service.status`      | `service`（必填）| systemctl status，macOS 用 ps 兜底     |

## 安全约定

- 所有样例均为 `permissionProfile: read-only`，无副作用。
- 参数通过 argv 传递（`$1`），禁止拼接 Shell；脚本内使用白名单校验。
- 不要在任何 manifest / 可执行文件中放置 Secret。
- 新增命令后运行一次 Agent（或重启实例），命令快照会重新上报。

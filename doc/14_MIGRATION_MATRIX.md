# 旧 init 系统迁移矩阵（需求子任务 1）

来源：`/Users/inori/data/ugit/init`（只读输入，不复制脚本，仅提取行为/变量/数据目录/备份恢复方法）。
目标：ServerCLI 声明式多节点运维（模块 = module.yaml + modman Runner，身份 = cluster_id+node_id+密钥，禁止 IP/MAC 身份）。

## 节点清单（MAC 仅迁移元数据）

| 别名 | 公网 IP | MAC（legacy 元数据） | 角色 | 承载服务 |
|------|---------|----------------------|------|----------|
| inori (TENGXUN) | 43.142.105.236 | 52:54:00:63:d8:4a | ServerCLI primary | Gitea、WireGuard、Caddy、Git、Vault、Vaultwarden、Jenkins、Mongo、MariaDB、Postgres、Qdrant、MinIO、Redis、NewAPI、Linkwarden、n8n 部分 |
| asuna (ALIYUN) | 218.244.159.177 | 00:16:3e:2a:b2:49 | ServerCLI aliyun-1（DB/MinIO/Redis） | Memos、Hedgedoc、Lobehub、AIHub、DBX、MinIO、OSS 内部 |
| yuuki (TENGXUNRIBEN) | 43.167.172.45 | 52:54:00:66:6f:fe | ServerCLI japan-1 | n8n、CLI Proxy API |
| qunhui (群晖 NAS) | （内网） | 02:42:ac:11:00:02 | 备份目标 | 群晖备份 |

## 服务迁移矩阵（按确认批次）

| 批次 | 服务 | 安装脚本 | refresh | update | backup | restore | 数据目录/方法 | rollback_type | 迁移类型 |
|------|------|----------|---------|--------|--------|---------|----------------|---------------|----------|
| 1 Foundation | v2ray | install_v2ray.sh | - | update_docker.sh | backup_oss.sh | - | 配置 + 代理 | config | adopt/rebuild |
| 1 Foundation | docker | install_docker.sh | - | update_docker.sh | - | - | systemd/docker | config | rebuild |
| 1 Foundation | caddy | install_caddy.sh | refresh_caddy.sh | update_docker.sh | - | - | /home/docker/caddy、Caddyfile | config | adopt/rebuild |
| 2 | servercli | install_servercli.sh | - | update_servercli.sh | backup_centos_init.sh | restore_serverCli.sh / restore_after_serverCli.sh | 二进制 + /etc/servercli + state | config | adopt（控制面纳管自身） |
| 2 | gitea | install_git.sh | refresh_git.sh | update_docker.sh | backup_git.sh | restore_after_git.sh | /data/gitea-dump.zip、/var/lib/gitea | data | adopt（保留 MariaDB） |
| 3 | postgres | install_postgres.sh | refresh_postgres.sh | update_docker.sh | backup_postgres.sh | restore_after_postgres.sh | pg_dump | data | adopt |
| 3 | mariadb | install_mariadb.sh | refresh_mariadb.sh | update_docker.sh | backup_mariadb.sh | restore_after_mariadb.sh | mysqldump | data | adopt |
| 3 | mongo | install_mongo.sh | refresh_mongo.sh | update_docker.sh | backup_mongo.sh | restore_after_mongo.sh | mongodump | data | adopt |
| 3 | redis | install_redies.sh | - | update_docker.sh | - | - | RDB/AOF | data | adopt |
| 4 | minio | install_minio.sh | refresh_minio.sh | update_docker.sh | backup/minio.sh | restore_after_minio.sh | mc mirror | data | adopt |
| 4 | qdrant | install_qdrant.sh | refresh_qdrant.sh | update_docker.sh | backup_qdrant.sh | restore_after_qdrant.sh | snapshot/目录 | data | adopt |
| 4 | vault | install_vault.sh | - | update_docker.sh | backup_vault.sh | restore_after_vault.sh / restore_vault.sh | raft/snapshot | data | adopt |
| 5 | n8n | install_n8n.sh | refresh_n8n.sh | update_docker.sh | backup_n8n.sh | restore_n8n.sh | /home/data + DB | data | adopt |
| 5 | new-api | install_new-api.sh | refresh_new-api.sh | update_newapi.sh | backup_new-api.sh | restore_new-api.sh | DB + 配置 | data | adopt |
| 5 | aihub | install_aihub.sh | refresh_aihub.sh | update_aihub.sh | - | - | DB + 配置 | data | adopt |
| 5 | cli-proxy-api | install_cli-proxy-api.sh | refresh_cli-proxy-api.sh | - | backup_cli-proxy-api.sh | restore_cli-proxy-api.sh | 配置 | data | adopt |
| 5 | memos | install_memos.sh | refresh_memos.sh | update_docker.sh | - | - | 数据目录 | data | adopt |
| 5 | hedgedoc | install_hedgedoc.sh | refresh_hedgedoc.sh | update_docker.sh | - | - | DB + 数据 | data | adopt |
| 5 | lobehub | install_lobehub.sh | refresh_lobehub.sh | update_docker.sh | - | - | DB + 数据 | data | adopt |
| 5 | linkwarden | install_linkwarden.sh | refresh_linkwarden.sh | update_docker.sh | - | - | DB + 数据 | data | adopt |
| 5 | dbx | install_dbx.sh | - | - | - | - | DB + 数据 | data | adopt |
| 6 | jenkins | install_jenkins.sh | refresh_jenkins.sh | - | backup_jenkins.sh | restore_jenkins.sh | /data/jobs | data | adopt |
| 6 | wireguard | install_wireguard.sh | refresh_wireguard.sh | - | backup_wireguard.sh | restore_wireguard.sh | wg 配置 | config | adopt |
| 6 | rclone | install_rclone.sh | refresh_rclone.sh | - | - | - | rclone.conf | config | adopt |
| 6 | 群晖备份 | - | - | - | backup_qunhui.sh | - | 群晖 NAS | data | manual |

## 配置来源与 Secret 变量

- 配置来源：`/home/init/centos/common/config.sh`（MAC/IP/仓库/路径/版本变量）、各 `install_*.sh` 内的变量赋值、`common/conf` 目录。
- Secret 变量：`common/secrets.sh`（GIT_USERNAME/GIT_PASSWORD、OSS AK、数据库密码、Webhook 等）。第一版严禁将其中真实凭据复制到 serverCli 仓库/测试夹具/日志/Release；仅生成 SecretRef 清单，值走受控导入流程。
- 端口/依赖/健康检查：以各 docker-compose 与 refresh 脚本为准，迁移时逐服务录入 ModuleDefinition。

## 迁移批次与验收顺序

1. v2ray、Docker、Caddy（Foundation，OSS-first bootstrap 内安装）
2. ServerCLI、Gitea
3. PostgreSQL、MariaDB、MongoDB、Redis
4. MinIO、Qdrant、Vault
5. n8n、NewAPI、AIHub、CLI Proxy、Memos、Hedgedoc、Lobehub、Linkwarden、DBX
6. Jenkins、WireGuard、Rclone、群晖备份

每批必须通过：新机 init → 旧机 adopt → update → backup → restore → rollback → OSS 断网恢复 → Agent 重启恢复。

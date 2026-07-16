# Octo CLI 从 Marketplace 安装 Skill 方案

状态：第一阶段方案
日期：2026-07-15

## 1. 目标

支持 Agent 使用现有 `octo-cli` Bot 凭证，从 Octo Marketplace 下载并安装一个指定 Skill。

第一阶段只实现最小闭环：

```text
octo-cli
→ Marketplace 下载接口
→ Marketplace 调用 octo-server 验证 Bot Token
→ 下载完整 Skill 制品
→ CLI 校验并安装到指定 Skills 根目录
```

## 2. CLI 命令

正式命令：

```bash
octo-cli marketplace skills <skill-id> --install <skills-root>
```

示例：

```bash
octo-cli marketplace skills 7dcd8c9d-0000-4000-8000-000000000001 \
  --install ~/.cc-channel-octo/skills
```

提供短别名：

```bash
octo-cli market skills 7dcd8c9d-0000-4000-8000-000000000001 \
  --install ~/.cc-channel-octo/skills
```

`--install` 的值始终是 Skills 根目录。实际安装目录由 CLI 拼接为：

```text
<skills-root>/<skill-name>/
```

## 3. 与现有命令的关系

现有离线命令保持不变：

```bash
octo-cli skills
octo-cli skills <name>
octo-cli skills --install <dir>
```

职责边界：

| 命令空间 | 数据来源 | 用途 |
| --- | --- | --- |
| `octo-cli skills` | CLI 内嵌 Skill | 离线查看、批量安装内嵌 Skill |
| `octo-cli marketplace` | 远程 Marketplace | 认证后下载并安装市场 Skill |

不修改或废弃现有 `skills --install`，避免破坏 CC Channel 等已有部署脚本。

## 4. Marketplace 地址

CLI 复用统一 API 域名，并单独配置 Marketplace 路径前缀：

```bash
export OCTO_API_BASE_URL=http://127.0.0.1:8092
export OCTO_MARKETPLACE_API_PREFIX=
```

生产环境示例：

```bash
export OCTO_API_BASE_URL=https://api.example.com/api
export OCTO_MARKETPLACE_API_PREFIX=/market
```

CLI 复用 `OCTO_API_BASE_URL` 的 scheme 和 host，并用 Marketplace 前缀替换
普通 API 路径：

```text
OCTO_API_BASE_URL=https://api.example.com/api
OCTO_MARKETPLACE_API_PREFIX=/market
→ https://api.example.com/market/api/v1/skill/:id
```

开发、测试、生产环境通过现有 Profile 或 `OCTO_API_BASE_URL` 切换域名，
`OCTO_MARKETPLACE_API_PREFIX` 只描述路由差异。网关负责将 `/market` 前缀
移除后转发到 Marketplace 服务的 `/api/v1`。

CLI Profile 继续只保存统一 API 地址：

```bash
octo-cli auth login \
  --profile production \
  --bot-id <robot-id> \
  --api-base-url https://api.example.com
```

显式设置的 `OCTO_API_BASE_URL` 优先于 Profile。

Marketplace 地址不硬编码到 `OCTO_API_BASE_URL`，以支持独立部署和本地联调。

## 5. 下载 API

```http
GET /api/v1/skill/:id/download
Authorization: Bearer bf_xxx
```

第一阶段只接受 `bf_*` User Bot Token。

成功响应由现有 Skill 下载接口返回：

```http
HTTP/1.1 302 Found
Location: <local-or-oss-download-url>
```

最终下载制品为 Marketplace 发布流程保存的 ZIP，必须包含完整 Skill 目录内容，例如：

```text
SKILL.md
references/
scripts/
assets/
```

建议错误语义：

| 状态码 | 含义 |
| --- | --- |
| `400` | Skill 名称或请求参数无效 |
| `401` | Bot Token 缺失、无效或过期 |
| `403` | Bot 或所属 Space 无权下载该 Skill |
| `404` | Skill 不存在或对调用者不可见 |
| `503` | `octo-server` 认证服务不可用 |

对于无权限和不存在的私有 Skill，可以统一返回 `404`，避免泄露资源存在性。

## 6. 认证链路

CLI 不单独调用验证接口。下载接口自身完成认证和授权：

```text
octo-cli
  │ Authorization: Bearer bf_xxx
  ▼
Marketplace GET /api/v1/skill/:id/download
  │
  ├─ POST octo-server /v1/auth/verify-bot
  │    {"bot_token":"bf_xxx"}
  │
  ├─ 获取 bot_uid、owner_uid、space_id
  ├─ 将 owner_uid 和 space_id 写入统一认证 Context
  ├─ 复用现有 Skill Service 校验可见性
  └─ 返回 Local/OSS 下载地址
```

`/v1/auth/verify-bot` 是 `octo-server` 已有的生产认证接口，不是测试接口。Marketplace
可以短期缓存成功的验证结果，例如 30 秒。

### 6.1 为什么第一阶段只支持 `bf_*`

现有 `/v1/auth/verify-bot` 查询 `robot.bot_token`，可直接验证 `bf_*` User Bot。

`app_*` App Bot Token 存储于 `app_bot.token`，当前 `/v1/auth/verify-bot` 不覆盖该路径。
为了避免第一阶段改造 `octo-server`，Marketplace 安装命令遇到 `app_*` 时直接返回明确错误：

```text
Marketplace Skill installation currently requires a bf_* User Bot credential.
```

未来确实需要支持 App Bot 时，再扩展 `octo-server` 的统一 Bot 验证契约。

## 7. CLI 安装流程

```text
1. 从 octo-cli 现有 credential/profile 解析 bf_* Token
2. 请求 Marketplace 下载接口
3. 读取受大小限制的响应体，并在安装根目录创建临时目录
4. 计算 SHA-256，与详情接口的 `file_sha256` 比较
5. 安全解压 ZIP
6. 验证 SKILL.md 存在且 Skill 目录名与请求名称一致
7. 使用详情接口返回的 Skill 名称，原子替换 `<skills-root>/<name>/`
8. 输出 JSON 成功包络
```

必须执行的安全检查：

- 拒绝绝对路径。
- 拒绝包含 `..` 的路径。
- 拒绝解压后逃逸目标目录。
- 拒绝符号链接和硬链接。
- 限制压缩包大小、文件数量及解压后总大小。
- 下载或校验失败时不得破坏已安装版本。
- 只替换目标 Skill 目录，不触碰同根目录下的其他 Skill。

安装结果沿用 `octo-cli` JSON envelope，`data` 建议为：

```json
{
  "source": "marketplace",
  "name": "octo-docs",
  "installed_to": "/home/user/.cc-channel-octo/skills/octo-docs",
  "sha256": "...",
  "files": [
    "SKILL.md",
    "references/common.md"
  ]
}
```

## 8. 代码改造位置

### 8.1 octo-marketplace

```text
internal/auth/bot_resolver.go
internal/middleware/auth.go
internal/api/router/router.go
```

改造内容：

- 增加 `bf_*` Bot Token Resolver。
- 用户 Token 与 Bot Token 在统一认证中间件中按凭证类型分流。
- Bot 的 `owner_uid` 映射为业务身份，权威 `space_id` 写入请求 Context。
- 下载及后续管理接口复用现有 Skill Service 和 Local/OSS Storage。

### 8.2 octo-cli

```text
cmd/marketplace.go
internal/marketplace/client.go
internal/skillinstall/installer.go
internal/config/config.go
```

改造内容：

- 增加 `marketplace` 命令及 `market` alias。
- 增加 `OCTO_MARKETPLACE_URL`。
- 复用现有 Factory、credential、输出 envelope 和通用 flags。
- 下载、校验、安全解压并原子安装指定 Skill。

## 9. 第一阶段明确不做

- Marketplace 搜索和列表命令。
- Skill 卸载、升级和版本选择。
- global、Space、user、agent 多层策略自动同步。
- plan、sync 和 lockfile。
- 自动识别 Codex、Claude 或 CC Channel 目录。
- MCP 安装。
- `app_*` App Bot 支持。
- `octo-server` 改造。

## 10. 验收标准

- 使用有效 `bf_*` 凭证可下载并安装指定 Skill。
- 未配置凭证时 CLI 返回认证错误且不创建目标目录。
- `app_*` 凭证返回明确的不支持错误。
- 无权限或不可见 Skill 不泄露其存在性。
- SHA-256 不匹配时安装失败，原有 Skill 保持不变。
- 恶意归档路径、链接和超限归档被拒绝。
- `SKILL.md`、references、scripts、assets 均能完整安装。
- 现有 `octo-cli skills --install <dir>` 行为及测试保持不变。

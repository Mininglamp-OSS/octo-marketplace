# MP-03：统一原子安装、A34 最新完整内容与安全重试（B 模型）

> 决策依据：[00-decisions](../00-decisions.md) B 模型、D3（无共存/原名原字节）、D6（坏图失败）、D7（Connector effective）、D9（同 key 原 plan、新 key 最新）、D10（旧执行器退役由 REL-01 收口）。Fleet 只沿用既有幂等安装，不增查询、不改源码/API/表/保留或过期重占。

## 目标（Goal）

硬依赖 MP-02（传递依赖 MP-01）。范围为一个 `InstallCoordinator` 安装引擎、首次冻结 plan、窗口内同 key 同完整请求重试、新 key 按最新解析，以及 A34 无副作用完整内容读取。A34 按 B 每次返回“当前最新解析图 + 全部原始文件”，`bundle_digest` 动态；同一 Expert/Team 版本不同时间读取/安装可能拿到不同依赖内容，这是 B 的明确代价。

范围：octo-marketplace；Fleet 为联调依赖。非目标：无凭据重启查询、跨 24h 自动恢复、长期幂等保留、改 Daemon 市场解析、工作空间绑定系统、自动刷新已安装 MCP、强停实例、二进制 Skill 支持。**保留 A31/A32 两个 URL，不等于保留两个执行器或旧行为。**

## 背景（Background）

B 下 Expert→Skill、Team→Expert 在每次读取/安装时解析目标当前正式发布版；Connector 按 ID 读 effective。A34 与安装必须复用 MP-02 的 `LatestPublishedResolver`，每次请求各自使用一致性快照；不存在“固定 pin”或“失败后补 latest/切旧版”的分支。

D9 明确重试语义：同一安装意图（同 `Idempotency-Key`）第一次请求解析并冻结 plan/请求/HMAC；同 key 重试**只重放首次 plan**，不重新读最新配置或最新依赖。本次显式安装输入变化时返回 409；仅上游正常发新版不阻断原 key 重试。用户想使用最新版时用**新 key** 发起一次新安装，新安装才重新解析最新。这样“允许按最新配置来”落在新意图上，不会与上一次 UNKNOWN 结果打架，也不需要改 Fleet 协议。

D3 明确本期不允许同名/多版本共存：安装必须保留市场原名/原字节；同名不同完整内容冲突失败；不生成 `mk1` 别名、不改写 `SKILL.md` frontmatter、不改目录名；不改 Fleet/Daemon。读取成功不代表 Fleet 安装或共存成功。

## 承重行为（Load-bearing）

1. **A34 是唯一完整内容入口。** `GET /market/api/v1/plugins/{plugin_id}/distribution` 只接受 path `plugin_id` + 既有认证/Space；无 workspace/runtime/custom_env/幂等键/根版本选择。每次按 B 解析最新完整图：Expert/Team 自身 + 最新成员 Expert + 最新 Skill + 有效 Connector。
2. **A34 一致性与失败关闭。** 同一短只读一致性事务解析根/refs/effective，退出事务后读不可变文件并校验；`no-store`、零 Fleet、零写入。每个 `node_key` 恰一 asset；文件原字节/hash；Connector `files:[]` + 有效公开配置/占位符。16MiB 完整 JSON 与图/文件预算；缺节点/文件/权限/完整性整体失败；不分页/裁剪/补 latest（“最新”是模型，不是失败回退）。
3. **A34 不产安装物。** 不生成 `installation_name`、不改写 frontmatter、不产安装副本；原市场名称/字节不变。`bundle_digest` 排除自身、按契约排序并 JCS 计算，随最新依赖动态变化。
4. **一个安装引擎。** A31/A32 共用唯一 `InstallCoordinator` + attempt/状态机；A32 保留 URL，但要求 `Idempotency-Key`/用户身份/Workspace UUID，旧客户端升级前会 400 并带明确 hint。删除市场 legacy create/bind/delete 补偿与 fallback；独立非市场 `/experts`、`/squads` 入口保留共享实现，不新增 Plugin 映射。
5. **首次冻结 plan。** 新 key 第一次请求在一个一致性快照内解析最新图、冻结节点版本/effective revision/文件清单/序列化请求与 HMAC；后续同 key 只重放该 plan，不重新解析。新 key 新安装重新解析最新。
6. **同 key / 新 key 语义（D9）。** 同 key 同规范化输入（caller/Space/Workspace/runtime/plugin/resource_name/custom_env/root）→ 回放原 plan/原 attempt；同 key 不同输入 → 409 `idempotency_input_mismatch`，不自动改 plan；显式安装输入变了 → 409；仅上游正常发新版仍重放原 plan，UI 可提供“重新安装（新 key，最新配置）”；新 key → 新 attempt、新解析、新 plan。**任何情况下都不在失败后静默补 latest、换 key、切旧执行器。**
7. **状态机与 UNKNOWN。** `PREPARED→DISPATCHING→SUCCEEDED/FAILED/UNKNOWN`，CAS + 发送租约；首次可能发送前固定 `first_dispatched_at/retry_until`。租约到期或进程中断只能继续原 attempt，保留“可能已发送”证据，不恢复成“尚未发送”；晚到成功可幂等收敛，旧租约错误不得覆盖较新成功。仅第一次 `SUCCEEDED` 转换与本地安装计数同事务；同键重放/竞争失败不重复计数。
8. **失败不回退。** 依赖最新版下架/删除/缺失、缺文件、摘要不符、预算/MCP 冲突都在准备阶段整体失败，Fleet mutation=0；不回退旧版本、不删边、不空配置、不跳文件。
9. **原名/原字节、无共存。** 普通安装使用市场原名/原字节；同名不同完整内容（同 Workspace/owner 或同 Definition）明确冲突失败，不自动改名或覆盖。不实现 `mk1`、frontmatter/BOM 改写、目录名改写、Fleet/Daemon 改造。
10. **Connector/MCP。** 按各 Expert 关系顺序合并，同 server key 拒绝，复用 transport/字段/大小校验；安装/A34 读 effective，不读未批准 editing。无 Connector 或合并后为空时省略 `mcp_config`，不发 `{"mcpServers":{}}`；plan 不含 custom_env/认证值，错误只指出字段位置。
11. **无新增结果查询。** 不新增安装结果 GET、资源名称查找或 Fleet 长期回执任务；已有本地成功可回放，未知记录不得当成远端未执行。

## 非目标（Out of scope）

A09 restore、A29/A30 HTTP、同名/多版本共存、`mk1` 别名、frontmatter 重写、Fleet/Daemon 改造、安装结果查询/跨重启无凭据恢复、自动刷新已安装 MCP、强停实例、二进制 Skill。遗留历史转换归 MP-04；四端接入归 CL-02/CL-03；生产切换归 REL-01。

## 源码与数据所有权

- Marketplace：`internal/service/plugin/{install,capability_install,capability_definition,capability_assets,capability_mcp}.go`，最新解析调用与 `InstallCoordinator`；`internal/fleet/{capability,capability_types,client}.go`；handler/plugin/install 及 installations。仅退役市场旧 adapter 及其专用调用；独立非市场 Expert/Squad 入口仍使用的 `service/expert` 共享实现保留，不新增 Plugin 映射。
- 唯一创建 Marketplace 的 `plugin_install_attempts` 及无运行时凭据计划存储，扩展对象保留检查以保护 plan 引用。
- 唯一拥有并实现 A31/A32/A34 端点、DTO 与完整内容读取组装；MP-01 提供版本文件底座，MP-02 提供唯一最新解析器/refs 底座。F01 仅是已存在的 Fleet 依赖协议，不拥有其服务端实现，不创建/修改 Fleet 的 receipt 表、action、scheduler、OpenAPI 或迁移。
- 无新增安装结果 GET；原评审稿的查询恢复和 Fleet 长期回执任务已撤回，不能另加临时 lookup 接口。

## 实现步骤

1. 安装与 A34 共用底层内容组装：从当前 published 根出发，调用 MP-02 的 `LatestPublishedResolver` 解析最新成员 Expert/Skill/Connector effective，按资源+版本去重、批量读取、请求内缓存。同一短只读一致性事务选定根、全部最新版本和 effective，并复制 Connector 完整 effective 内容及 revision/digest；不能先记 revision 再从被覆盖的单行取内容。结束事务后读取不可变对象并核对清单，不在数据库锁内做 OSS/Fleet I/O，不跨请求缓存授权。保留现有权限/上下架及历史受众检查，不新增下架豁免。
   - 可复用 `capability_definition.go` 的 `buildCapabilityInstall` 与 `capability_assets.go` 的 `skillFiles` 读取校验，但须抽出不依赖 Runtime/Workspace/Secret 的内容组装；旧实现按 ID/current、inline MCP 和 legacy ZIP/ref 读取，不能原样暴露或将 legacy stub/外部入口文档替换策略带入新快照。Fleet 序列化及安装命名是另一个阶段，且本期不做共存命名。
   - 图预算与 MP-02 统一为含根 1500 节点/2000 边，在新路径替换旧 `install.go` 的 500 目标 guard；每次读取增量计数。按清单预检并边读累计 ≤16MiB 原字节，最终序列化按完整 JSON 预算验收，不先无界加载全部 OSS 内容。F01 所有字符串/路径/摘要等重新按现有 Fleet 限制校验，不截断市场内容以适配。
2. **首次解析并冻结 plan。** 新 key 第一次请求解析当前最新完整图，冻结每个节点的 `plugin_id@version_id`（Connector 为 `plugin_id@effective_revision`）、文件清单/hash、规范化序列化请求与 HMAC。冻结后同 key 的重试不再调用解析器；即使期间 Skill/Expert 发布新版、Connector 产生新 effective，也只用首次 plan（D9）。plan 只存无 Secret 意图与内容引用。
   - 新 key 新安装重新解析最新，允许得到与旧 key 不同内容；UI/客户端必须提示“新安装使用最新依赖”，不能把旧 plan 复制给新 key。
   - 首次 dispatch 前校验冻结 plan 中每个具体节点仍可取/未删除；冻结的具体版本被删除、或来源被下架/删除导致节点不可用时返回 409 `dependency_changed`，不静默换最新版本；用户修复后用新 key 重装。
3. **同名冲突失败。** 安装前按现有 Fleet 约束与实际 Workspace/Definition 校验名称；同名不同完整内容冲突明确失败，不生成 `mk1`、不改写 `SKILL.md` frontmatter、不改目录名、不覆盖已有资源。普通无冲突安装保留市场原名/原字节。需要改 Fleet/Daemon 才能共存时报告范围冲突，不把共存验证作为 A34 或普通安装的前置。
4. MCP 按各 Expert 关系排序合并，同 server key 拒绝，复用 transport/字段/大小校验。无 Connector 或合并后无 server 时省略 Fleet `mcp_config`，不能发送 `{"mcpServers":{}}`（现有 Fleet 拒绝空对象）。计划不含 custom_env 或认证值；只去掉 custom_env 不足以证明 MCP 无 Secret，按既有占位符约束拒绝疑似凭据，错误仅指出字段位置。
5. 先按 caller/workspace/key 查已有 attempt；已有经完整性校验的 `SUCCEEDED` 按权威 API §6 校验原 caller/Space/Workspace 访问资格后直接回放历史结果，不要求重传原 Secret 或根仍上架，也不宣称资源当前仍存在。其他状态核验原输入 HMAC 并读取原 plan；不存在才构建 plan 并原子抢占。输入覆盖 caller/Space/workspace/plugin/runtime/name/custom_env 和规范版本，密钥安全配置且记录 `key_id`。竞争失败者只能读胜出 plan，不发送自己的候选结果；有未决意图但本地记录丢失时不能默认当成首次安装。
   - 持久化归一化非 Secret 意图的 `intent_digest`，已知成功回放也必须匹配 root/Space/Workspace/runtime/resource_name；显式提供 custom_env 时再验完整 `input_hmac`，未提供不要求补 Secret。不能把“无需 Secret 回放”误作允许同 key 换目标参数。
6. `PREPARED→DISPATCHING→SUCCEEDED/FAILED/UNKNOWN`，用 CAS 和发送租约控制并发；重试只用原 root/variant/Connector 内容与首次 plan，不重读当前市场。首次可能发送前固定 `first_dispatched_at/retry_until` 和出站 HMAC；不能每次点击延长窗口。租约到期或 DISPATCHING 进程中断只允许继续原 attempt 并保留可能已发送的未知证据，不能恢复成“尚未发送”的 PREPARED；每次实际出站前重查截止时间。晚到的有效成功可幂等收敛到 SUCCEEDED，旧租约的超时/错误不得覆盖较新的成功或刷新窗口。
7. 两市场安装入口归一化到同一 Coordinator：A31 `POST /plugins/{plugin_id}/installations` 与 A32 `POST /plugins/install` 都要求 `Idempotency-Key`、权威用户身份、Workspace UUID；A32 结果仍 `agent_id`/`squad_id`，A31 同。移除 A32 逐资源 create-bind-delete 补偿、旧参数分支和 fallback；旧客户端缺头/缺用户返回 400 并带升级 hint。独立非市场 `/experts`、`/squads` 安装不在本期迁移，保留仍有独立 caller 的共享实现，只删除市场已无调用者的 adapter 及 dead code。
8. 第一次请求发出现有 F01。网络超时、断连或非确定响应记 UNKNOWN；窗口内客户端重新提供原完整参数/运行时凭据，Marketplace 用**首次 plan**重建完全相同请求并同 `fleet_key` 再次 POST。由现有 Fleet receipt 保证回放或并发仲裁，不先查询、不按资源名称猜提交结果。
9. HMAC 不匹配、无法重建原参数/凭据、plan 缺失或超过保守 24h 窗口时停止自动 POST，返回明确冲突/结果未知。既有 UNKNOWN 不会因后续 4xx 变成“已失败”；无凭据跨重启自动恢复不承诺，不能随机换 key 或切另一个入口。
10. Fleet 仍可能在其窗口到期后重占 key；本期不改变它。因此 Marketplace 必须在出站前检查自己从首次可能发送计时的截止时间，客户端也不能通过丢弃 pending 绕过。已验证成功的本地结果可回放，但未知记录不得被当成远端未执行。
11. 校验 FleetResult 类型、名称、唯一资源 ID、Skill 完整绑定及 Leader ID，首次 SUCCEEDED 转换与本地安装计数同事务；CAS 竞争失败或同键重放不重复计数，不从 `skills.created` 或缺少上游回放 Header 猜 replayed。未通过校验的 200 同样记 UNKNOWN，不能假成功。本地出站 HMAC 不与 Fleet 规范化 SHA-256 混比；不要求远端返回新摘要。
12. 实现只读 `GET /market/api/v1/plugins/{plugin_id}/distribution`（A34），只需 path `plugin_id` 和既有认证/Space 上下文，不要求 workspace/runtime/custom_env/幂等 key，不新增历史根选择器或版本 UI。200 为 `{data:DistributionBundle}`，保留 `schema_version/root/graph/assets/bundle_digest`。Expert/Team 由服务端按 B 解析全部最新成员/依赖；Skill/Connector 单根兼容已有本地入口。每个 graph `node_key` 恰好一项 asset，准确 id/version 由 graph 取得；`files:[{path,mime_type,content,content_hash}]` 内联全部 UTF-8 原始文件，Expert/Team 含自身 `AGENTS.md`，Skill 含 `SKILL.md` 和全部支持文件，路径/字节/hash 与选中清单一致。Connector 提供有效修订的公开声明/占位符 `mcp_config`，不含运行时凭据，files 固定为 `[]`；resolver 核验选定完整 effective 行的 digest，客户端不从只有 `mcp_config` 的 asset 伪重算整行 `effective_digest`。不得仅给 ID/链接/storage key 或要求客户端补取；不返回 `installation_name`、不重写 frontmatter、不生成安装副本，返回原市场内容。
13. A34 一次完整响应，不分页/截断/裁剪后 200，不 fallback latest（B 的“最新”是正常解析，不是失败回退）；预算与摘要逐字段遵守 API §6.1，16MiB 计最终 `{data:...}` JSON 的 UTF-8 字节（包括包络、元数据及转义），不能只统计正文。每 `node_key` 恰一 asset，版本化 root 的 `selected_version_id` 必须等于对应 Graph 节点 `version_id`，Connector 根 `effective_revision` 及 asset 配置必须与选定 Graph 节点一致。文件 hash 为原始 UTF-8 字节 SHA-256 小写 hex、无 `sha256:` 前缀；旧存储摘要若有前缀仅内部适配，不能改变 wire。`bundle_digest` 排除自身、按契约排序并 RFC 8785 JCS 编码后计算，不能使用 Go/JS 默认 JSON 差异定义摘要。缺节点/文件/权限/摘要异常整体失败，`budget_exceeded`=413、文件或 refs 完整性=500、权限沿用既有安全错误，错误也 `no-store`。查询不调用 Fleet、不创建 attempt/安装记录/资源、不改发布状态。A34 成功不等于安装成功，也不证明 Daemon 加载共存。

## 接口边界与失败语义

- A34 请求/响应与错误以 [API 契约 §6](../02-api-contracts.md) 为准：只 path ID + 既有认证/Space；无 workspace/runtime/custom_env/Idempotency-Key；`Cache-Control: no-store`；错误响应同样 `no-store`。缺节点/文件/摘要/权限整体失败，无残缺 200。
- A31 body 为 `runtime_id/resource_name?/custom_env?`，A32 body 保持 `plugin_id/workspace_id/runtime_id/custom_env?`；两者都要求 `Idempotency-Key`、权威用户身份、Workspace UUID，结果保留 `agent_id`/`squad_id`。客户端不得靠 GET 查结果，不得把 `installation_in_progress`/`expired`/`input_mismatch`/`unknown` 混成“同名冲突”。
- 同 key 同输入回放原 attempt/plan；同 key 不同输入 409 `idempotency_input_mismatch`；冻结 plan 的节点已删除/下架 409 `dependency_changed`；首次成功计数一次。超窗/无法重建返回安全错误并保留 UNKNOWN，不换 key、不删记录、不切入口。
- 缺依赖/文件、摘要不符、二进制、预算或 MCP 冲突等准备错误 Fleet mutation=0；不生成空能力或删文件后成功。Secret/Token/custom_env 不进入 attempt/plan/日志/错误/命令回显；HMAC 密钥轮换仍可验证相关原输入。
- 本 issue 不注册 A09、A29、A30；不存在 restore、legacy_import HTTP 或旧执行器兼容分支。

## 验收（Acceptance）

- [ ] **B 读取：** Skill S 发布 v2 后，引用它的 Expert/Team 下一次 A34 返回 S v2 及对应文件，`bundle_digest` 变化；Expert/Team 自身版本未变。Team 的成员角色/Leader/顺序仍来自 Team 边。
- [ ] **B 不回退：** 最新 S v2 下架/删除后 A34 整体失败（或来源不可用），不得返回 S v1；恢复合法发布后下一次读取恢复最新。无“失败补 latest/切旧版”分支。
- [ ] **同 key / 新 key（D9）：** 新 key 首次安装冻结 plan；期间 Skill/Expert 发新版或 Connector 产生新 effective，同 key 窗口内重试仍用首次 plan/原内容；用新 key 再次安装才重新解析最新。根/Connector 升级不改变原 key 的计划。
- [ ] 两并发同 key 仅选一份 plan，换参数 409 `idempotency_input_mismatch`；两个外部入口共用 attempt，不能跨 URL 重复创建。冻结节点被删除/下架时 409 `dependency_changed`，不静默换最新。
- [ ] DISPATCHING 发送前/后崩溃、租约到期与晚到响应均不创建新 attempt；成功终态不被旧错误覆盖，第一次成功 CAS 与计数原子提交。Fleet 缺回放 Header 不妨碍本地同 attempt 成功幂等收敛。
- [ ] 缺依赖/文件、摘要不符、二进制、预算或 MCP 冲突等准备错误 Fleet mutation=0，不生成空能力或删文件后成功；缺依赖不回退旧版。
- [ ] Fleet 成功提交但响应丢失，窗口内重传原 key、原完整参数和凭据后返回原 agent/squad ID；无重复资源和安装计数。
- [ ] 首次 POST 仍在途时原 key 重试由现有 Fleet 约束收敛；不新增安装结果/恢复 GET、资源名称查找或其他执行器。A34 只是完整内容读取，不参与安装重试仲裁。
- [ ] 改原 Secret/参数、缺原 Secret、窗口到期、重启后仅剩 key 均不能自动重新安装；保留 UNKNOWN 并给安全提示，不自动换 key。
- [ ] 人为设置本地 `retry_until` 过期后，确认 Marketplace 零出站 POST；不靠修改 Fleet 过期行为实现这一保证。
- [ ] 先前 UNKNOWN 后收到 4xx 仍保留未知证据；已验证成功结果可回放，但不能把没有本地结果推断为未提交。
- [ ] Token/custom_env/认证 Header 值不进入 attempt/plan/日志/错误；HMAC 密钥轮换仍可验证相关原输入。
- [ ] 两 URL 只走同一 Coordinator/Capability 引擎，失败没有 legacy create/bind/delete；共享 Skill 不被本次补偿删除。
- [ ] **无共存：** 同名不同完整内容安装明确冲突失败，不生成 `mk1`、不改写 `SKILL.md` frontmatter/BOM、不改目录名、不覆盖已有资源；普通无冲突安装保留市场原名/原字节。Fleet/Daemon 无改造。
- [ ] 现有权限、发布审核与上下架访问检查不变，缺依赖失败无降级；Connector/A34 读 effective，未批准 editing 不泄漏。
- [ ] 一次 A34 GET 取得 Expert/Team 完整根、最新解析图、全部文件及有效 Connector，逐 `node_key` 与快照清单核对路径/字节/hash；同一 Skill 被两个成员引用时解析到同一最新版本并映射到同一 `node_key`，原市场名/frontmatter 未变，无 `installation_name` 或安装副本。
- [ ] 读取期间并发发布新根或更新 Connector，A34 仍返回同一一致性快照内的选定版本和同一 effective 修订的完整配置，根/graph/assets 无混版；16MiB 按转义后完整包络计，JCS 摘要及无前缀文件 hash 可由跨语言实现复核。
- [ ] A34 不需运行态参数，响应 `no-store`；缺文件/错 hash/无权/超预算分别整体失败，无残缺 200、无 latest 补齐；Fleet 请求、安装资源/记录和发布状态变更均为零。
- [ ] A34 成功不证明 Fleet 安装或 Daemon 共存成功；同名共存不列为本 issue 验收，也不因此阻塞完整内容读取。

## 验证与交付（Verification）

使用隔离 Marketplace MySQL、对象存储和未修改的真实 Fleet PostgreSQL/Workspace/Runtime：注入第一次成功 POST 响应丢失，保留原参数和凭据、更新 Connector 后**不要**在预期成功路径重新解析，而是在原窗口内同键再次 POST，核对返回 ID、资源/绑定/计数不重复且仍用首次 plan。再用新 key 发起一次新安装，核对解析到最新内容。另改变参数、移除原凭据、将本地截止时间设为过去，确认均不向 Fleet 发新安装。不得将 Fleet 的直接数据库观察包装成生产查询 API。

A34 单独 HTTP smoke：在隔离 Marketplace MySQL/OSS 中准备 Team→两个最新 Expert、共享同一最新 Skill、支持文件及有效 Connector（另有未批准编辑）；仅带既有认证/Space 上下文发一次 GET，核对 200 DTO、全部节点文件及有效修订、不含凭据，记录请求数与读取前后数据库/对象摘要。随后分别注入缺节点/文件、hash 错误、无权和 16MiB 预算越界，核对整体错误状态/`no-store`、无裁剪、Fleet 零调用及零业务写入。另验证同一 Team 在成员 Expert/Skill 发新版后再 GET 会返回新内容且 `bundle_digest` 改变。此为实现阶段待执行验收，本轮不运行或宣称通过。

只运行 Marketplace 变更对应的包/全量测试及 OpenAPI check/diff；Fleet 保持现有实现，用真实服务作联调证据，不提交 Fleet migration/action/cleanup/新接口。上线前确认目标 Fleet 现有 24h 窗口和配置，客户端幂等接入由 CL-02/CL-03 完成，统一切换与旧执行器退役由 REL-01 负责。

## 本轮评审修订验收

- [ ] 首次计划解析技能 v1，网络超时后发布 v2；同 key 同输入按原 v1 计划继续，新 key 新安装解析 v2。显式输入变化 409 idempotency_input_mismatch；原节点下架/删除/无权单独失败，UNKNOWN 不被清除或误当未提交。
- [ ] 列表请求看见 v1 后发布 v2，后续新安装使用 v2 合法；一次安装计划内部不能混版。
- [ ] 基于 #126/#128 实际选定版本复用安全修复；改造 legacy 执行器、重新解析的 attempt、mk1 和 live Connector，不仅共享解析函数便声称统一引擎完成。

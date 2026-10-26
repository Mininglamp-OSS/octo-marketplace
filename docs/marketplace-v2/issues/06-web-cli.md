# CL-02：Web 与 CLI 的发布全流程、最新内容读取、mine 诊断与安全安装（B 模型）

> 决策依据：[00-decisions](../00-decisions.md) B 模型、D3（无共存/不改写）、D4（四字段诊断）、D8（CAS/relations）、D9（同 key 原 plan、新 key 最新）。A01 端点 owner 为 MP-01，MP-02 扩展最新解析/A01 谓词，MP-03 拥有 A34 与安装引擎；本 issue 只做 Web/CLI 接入。

## 目标（Goal）

- 保留 Web 现有“我的资产”、详情、版本、审核和“通过 Bot 发布/更新”交互，由 CLI 完成独立资产发布及 Expert/Team 引用流程；Expert/Team 操作不得顺带创建依赖。
- **完成硬依赖：MP-02、MP-03**（继承 MP-01）；版本/发布/审核部分可在 MP-02 后与 MP-03 并行开发，A34/安装部分完成前置为 MP-03 已部署。CL-01/CL-03 独立目录，最终四端联调由 REL-01 验收；不把服务端引擎实现塞入本 issue。
- 唯一接口来源：[02 API 契约](../02-api-contracts.md) 的 A01～A10、A12～A19（新快照 DTO 接入既有发布审核职责与访问检查）、A32、A34。以下 URL 省略 `/market/api/v1`；不自建 Web/CLI 协议，不新增安装结果查询。

## 背景（Background）

B 模型下 Expert→Skill、Team→Expert 每次读取/安装解析目标当前正式发布版，`target_version_id` 只是上次解析/审计值，客户端**不能**用它固定依赖。因此：

- Web 详情/“我的发布”必须区分“来源自己的版本”与“依赖目标的最新解析版本”；同一 Expert/Team 版本在不同时间可能展示不同依赖内容，A34 `bundle_digest` 会变化。
- mine 的 `dependency_availability` 四字段必须原样透传到全部/专家/专家团 tab，并区分 published/editing 诊断根与匿名原因；不能在前端重算。
- 安装同 key 只重放首次 plan；显式安装输入变化 409；上游正常发新版仍重放原 plan，想取最新版则用新 key 新安装，新安装才解析最新。

## 承重行为（Load-bearing）

1. **读投影。** 市场 `GET /plugins`、`GET /plugins/detail` 默认 published；“我的”编辑显式 `view=editing`，不把 `mode=mine` 当成编辑投影开关。Connector 市场读 effective、授权编辑读 editing。候选只取正式 published version；不得由 `current_version_id` 猜目标版本。
2. **A01 公共过滤（服务端）。** 缺省 mode 由服务端在 SQL 分页/COUNT 之前按最新解析过滤直接或间接依赖不可用的 Expert/Team；Team 沿最新成员 Expert 再判 Skill/Connector。分类 `plugin_count` 与公共 tags 同谓词。前端不二次过滤、不缩减 total、不逐卡请求依赖或 A34。
3. **mine 四字段。** `mode=mine` 保留原有权限内全部非删除资产、各 listing 状态与 total，不丢行；仅 Expert/Team 必返 `dependency_availability={status,evaluated_view,evaluated_version_id,reasons}`。`evaluated_view/evaluated_version_id` 指来源自己的 published/editing；已发布时诊断发布版，即使界面展示 editing；从未发布才评估 current。正常无依赖 `available` 且 `reasons=[]`，异常至少一条。
4. **原因展示。** `DependencyReason={code:'dependency_delisted'|'dependency_deleted'|'dependency_unavailable',target?:DependencyTarget,via?:DependencyTarget[]}`；`DependencyTarget={plugin_id,plugin_type,plugin_name,version_id?}`，Connector 不含 version_id；`version_id` 是本次解析到的最新目标版本。直接原因省略 via；间接 via 从根到目标列中间成员，不含根/目标。服务端已做匿名聚合，前端不得从缓存/名称补全。
5. **A34 唯一完整内容。** 完整 Expert/Team 内容、文件、成员角色与有效 Connector 只消费一次 A34；不先 A03 再扇出 detail/versions/skill_md/download/files。A03 仅独立轻量图。A34 的依赖内容会随最新版变化，缓存只限本次身份/Space/请求会话。
6. **保存/发布。** 保存走 A12 完整 Write：编辑先读 editing 完整内容/关系和 `edit_revision`，提交 `expected_edit_revision`；新建省略 CAS；显式传完整 `relations`（目标 ID，版本可省略/仅审计），省略数组错误、`[]` 才清空。发布 A13 指定 `version_id`；审核 A14 只提交 `{plugin_id,version_id,changelog?}`。依赖目标发新版不需要来源发新版，也不需要重新审核来源。
7. **审核观察。** A15 展示提交时的解析版本/Connector observations/baseline 差异；人工模式批准时依赖变化 409 `dependency_changed`，自动模式由服务端内部重试，前端不把 409 变成日常步骤。批准后依赖继续浮动，A34 下一次可能返回新内容。
8. **安装（D9）。** 保留 A32 URL + 必填 `Idempotency-Key`、权威用户身份、Workspace UUID；每次明确安装意图生成 key；同 key 双击/网络重试复用原 key、原 plan 与完整规范化请求；显式安装输入变了 409；仅上游正常发新版仍重放原 plan，想取最新版时 UI 提示“重新安装（新 key，最新配置）”；不自动换 key、不跨 URL fallback、不查远端结果。
9. **无共存改造。** 不实现 `mk1`、frontmatter/BOM 改写、ZIP 补文件或“改名共存”提示。完整内容使用 A34 原字节。

## 非目标（Out of scope）

完整 Web 编辑器、Admin 上传面、依赖绑定/选择器、Connector 版本系统；Bot 自行批准/绕过权限；在客户端重做服务端解析/事务；A29/A30 HTTP、同名/多版本共存、Fleet/Daemon 改造。

## 源码与所有权

- `octo-web/packages/dmworkmcp/src/api/{expertService,expertWire,pluginWire,mcpService,pluginReview}.ts`、`types/mcp.ts` 以及实际承载专家领域类型的 `mock/expertMock.ts`；`utils/{expertBotPublishPrompt,mcpBotPublishPrompt}.ts`、`components/{ExpertBotPublishModal,McpBotPublishModal,ExpertDetailModal,ExpertSkillBrowser,ExpertSpecView,ReviewSubmitModal,McpCreateModal,ExpertAddToLoopModal}.tsx`、`hooks/useMyReviewState.ts` 及既有我的资产动作。不能只更新 MCP 类型而遗漏被 expertWire/bridge/UI 实际引用的 ExpertItem/ExpertMember/ExpertSkill。
- `octo-web/packages/dmworkskillmarket/src/api/{skillApi,skillApiReal,pluginWire}.ts`、`types/skill.ts`；`utils/{botPublishPrompt,installPrompt}.ts`、`components/{BotPublishModal,InstallPromptModal,NewSkillModal,EditSkillModal,ReviewQueue,ReviewDetailDrawer}.tsx` 及 `pages/{SkillListPage,SpaceReviewPage}.tsx` 与相邻测试。Skill 上传/重传/元数据编辑和组织审核仍是已有表面。
- `octo-cli/internal/registry/specs/marketplace.json`、`cmd/service/{flags,run,identity}.go` 及 `cmd/service/marketplace_test.go`、`internal/client/client.go` 与相邻 transport 测试；`skills/octo-marketplace/{SKILL,skills,mcp,expert}.md`。优先在 registry 声明必需 `Idempotency-Key`，不造第二套 header 传输器。
- **“我的发布”准确源码地图：**入口 `octo-web/packages/dmworkmcp/src/pages/ExpertMarketListPage.tsx` 的 `toMineRow` 同时映射专家/专家团 tab；数据经 `bridge/useExpertCatalog.ts` 的 mine 分流、`api/expertService.ts::listPathReal`、`api/expertWire.ts` 的 `mapPluginAgentListItem`/`mapPluginSquadListItem` 到 `mock/expertMock.ts` 的实际 ExpertItem 领域模型。公共与 mine 均复用 A01，不只改 ExpertCard。
- **全部 tab 不可遗漏：** `octo-web/packages/dmworkmcp/src/pages/AllAssetsList.tsx` 经 `@dmwork/skillmarket` 导出的 `getMySkills(...,{pluginType:"all"})` 获取跨类型列表，再映射 `MineRow`；共享链路为 `packages/dmworkskillmarket/src/api/{skillApi,skillApiReal,pluginWire}.ts` → `types/skill.ts` → `components/MineTable.tsx`（`MineRow`），导出在 `src/index.tsx`。Expert/Team 诊断须完整穿过此链路。
- **文案与共享展示：**市场现有资源位于 `packages/dmworkskillmarket/src/i18n/{zh-CN,en-US}.json`（skillMarket namespace）与 `packages/dmworkmcp/src/i18n/{zh-CN,en-US}.json`（mcp namespace），沿用 `@octo/base` 的 t/useI18n 和 namespace 注册；不要误改 dmworkbase 通用 locale。更新 MineTable 及必要样式；服务端 wire 与前端 domain 只做显式映射，不从卡片名称、当前草稿或关联缓存推断原因。
- 本 issue 所有权为前端/CLI schema、transport、工作流提示与文档；不写数据库。版本/审核/依赖规则由 MP-01/MP-02 所有，A34/安装计划/attempt 由 MP-03 所有；不涉及 Fleet 源码、API 或数据库改造。
- **已核源码现状：**`expertService.ts` 以 ID-only 的 `expertSkillIndex/squadSkillIndex` 与 index 参数补 `skill_md/download`，`allSettled` 丢 404 成员/Skill 并读 Expert 旧 `mcp.json`；`ReviewSubmitModal` 仍 bumpPatch 后重传 Manifest/Package/relations；`mcpService.ts::buildConnectorUpsert` 与 Skill 元数据更新先读默认详情再全量写；`NewSkillModal` 的已发布 Skill 升级直接把 `parse_task_id` 送 review。上述均是本次真实切换点。

## 实施步骤

1. 统一类型与查询：市场 `GET /plugins`、`GET /plugins/detail` 默认 published；“我的”编辑读显式 `view=editing`。候选只取正式 published version；不得由 `current_version_id` 猜目标版本。Connector 市场读 effective、授权编辑读 editing。
   - Web“我的发布”的全部、专家、专家团列表均显式传 `mode=mine&view=editing`，包括 `getMySkills` 的跨类型通路；mode 只控制范围，`dependency_availability` 另按返回的 published/editing 评估根解释，不能用行的草稿内容推导线上是否恢复。
   - A01 公共缺省 mode 由服务端在 SQL 分页/COUNT 之前按最新解析过滤直接或间接依赖下架/删除/缺失的 Expert/Team；Team 沿最新成员 Expert 再判断 Skill/Connector，不读 current/旧版本替代。分类 `plugin_count` 及公共 tags 复用同谓词。前端不二次过滤、不缩减 total、不逐卡请求依赖或 A34。
   - A01 `mode=mine` 保留既有 owner/Space 权限内全部非删除资产的草稿、待审、发布和下架各状态，异常不丢行、不缩减 total，不新增回收站或纳入自己已删除资源；Admin 管理入口保留。只在该 mode 的 Expert/Team 必返四字段诊断，其他类型和公共列表省略。已存在 `published_version_id` 时诊断当前发布版，即使界面展示 editing 草稿；从未发布才评估 current 草稿。正常无依赖图返回 `available` 且 `reasons=[]`；`unavailable` 至少一条原因。
   - 全部、专家、专家团 tab 将服务端诊断映射到共享 `MineRow`：unavailable 增加独立“依赖异常”标记及 Tooltip，保留原 listing_state/display_status 发布/审核标记和既有操作入口，不建立新生命周期，不改来源 listing_state。固定 code 映射既有 i18n，不接受服务端整句/HTML；支持 hover 与键盘 focus，可列多个可见原因。有权直接例：“Skill「X」已下架，当前发布版本暂不在市场展示”；间接例：“成员专家「A」依赖的 MCP「B」已下架，专家团暂不在市场展示”；匿名例：“存在已失效或不可访问的依赖，请检查依赖配置”。`evaluated_view=editing` 须标“草稿依赖异常”，不得称已从市场隐藏。
   - 移除/替换依赖并发布新来源后下一次列表查询恢复；仅修草稿不恢复线上。目标最新版发新版或合法上架后下一次查询重新解析，不新增推送/轮询。列表 metadata available 不等于 A34 文件读取或安装成功。
2. A03 `GET /plugins/detail_graph` 仅作独立轻量图展示，节点以资源+本次解析版本、Connector 以 effective revision 区分，边保留 source/target 资源 ID。完整 Expert/Team 内容展示和 CLI 完整内容查询统一一次 A34 `GET /market/api/v1/plugins/{plugin_id}/distribution`，不先读图再扇出详情或文件；消费完整 `root/graph/assets`，核对每 `node_key` 一项 asset，全部 `AGENTS.md`、`SKILL.md` 与支持文件及有效 Connector 配置内联。UI 展示本次解析的目标版本、成员角色和顺序；错误不能显示为“无依赖/可安装”。
   - `ExpertDetailModal/ExpertSpecView/ExpertSkillBrowser` 直接接受本次 bundle 的 `node_key`、关系和 files；删除该完整内容路径的 `expertSkillIndex/squadSkillIndex`、按 expert/member/index 找 ID 再取 `skill_md/download`、JSZip 补下载与失败后只读 Markdown 的分支。展开支持文件只读已校验 `asset.files`，显示层可渲染 Markdown 但原始内容/hash 不变，不伪造 URL/storage 定位。完整内容缓存仅限本次身份/Space/请求会话，切换时取消旧读并清空，不跨意图用旧 bundle 补错；不因依赖目标发新版而假定 bundle 永久不变。
3. CLI registry 补齐 A34，只传 `plugin_id` 与既有认证/Space 上下文，不需 workspace/runtime/custom_env/幂等 key，不新增历史根选择器。保留版本详情、发布/保存历史区分、图读取、候选与安装重试；历史 Skill `/skill_md`、`/download` 仍传明确版本，但不可用于补齐完整 Expert/Team 查询，不补 latest。移除 Web 附件定位伪造的 `storage:{type:"s3"}`，不暴露对象 key；A34 不提供 `installation_name`、不重写原市场名称/frontmatter 或生成安装副本。
4. 更新所有 Bot 发布/更新提示和四份 CLI skill：先解析用户意图并展示发布预览，获得既有明确确认后再写。Expert/Team 只有自身 `AGENTS.md`；Skill 为独立完整包，Connector 独立保存有效配置声明；不再写 Expert 的 `mcp.json`、bundled Skill 或内联成员。
5. Expert/Team 来源发布只搜索并选择已有正式资产，展示目标 ID（及其当前解析版本）供用户确认；缺失时停止并说明需另行发起独立发布，不能顺带创建。用户明确发起 Skill/Connector/Expert 的独立发布时，先保存并正式发布/等待审核，完成后才能被下一层显式引用；Team 成员必须是已正式发布 Expert，带唯一 `member_key` 和 Leader、非空 role。来源只存目标 ID；目标后来发新版不需要来源发新版。不能拿待审核快照组装“已发布成功”的上层。
6. 保存走 `POST /plugins/upsert`：编辑先读 editing 完整内容/关系和 `edit_revision`，提交 `expected_edit_revision`；新建省略 CAS。显式传完整 `relations`，保留未删关系的 `relation_id/data` 与目标 ID；省略数组是错误、`[]` 才清空。`target_version_id` 可省略或仅作审计，客户端不得把它当固定依赖。CAS 冲突读回差异并重新确认，不能按任意 409 自动切另一写接口或覆盖。
   - 覆盖所有非 Bot 写调用：`expertService::updateExpertVisibilityReal`、`mcpService::buildConnectorUpsert/updateMcpReal`、`skillApiReal::updateSkill` 及其表单调用者均显式 editing 读取，保留原 attachments/relations/roles/order 及打开/确认时的 `edit_revision`。不能提交前拿最新 revision 给旧表单续签；不再把 published 内容当编辑基线。保存拟定 visibility 不改线上有效范围，已发布/待审仍可保存草稿的既定新底座不得被旧 `listed_requires_review` 的客户端禁用逻辑拦住；不改变 owner/审核者等权限。
   - 独立 Skill ZIP 保留上传→parse→`POST /plugins/import` 的完整文件服务端保存链，按 02 中该既有端点的版本/CAS 增量接入 `createSkill/updateSkill` 与 CLI `plugin.import`；不是把 parse 结果的 SKILL.md 一段拼成 A12 Package。版本文件清单/历史获取按 A04～A08 新包络和 scope 更新 `listVersions/mapVersion` 及所有详情/历史调用者。
7. A13 `POST /plugins/publish` 提交 `{plugin_id,version_id,version,changelog?}`，version 与指定快照一致。private 既有直接 release 不变，space 冻结版本并按现有 Space 策略自动批准或人工审核，system 继续走 Admin。A14 提交只传 `{plugin_id,version_id,changelog?}`；A15～A17 沿用原权限/入口，approve/cancel 空 body、reject 必需 reason。Connector 省略 version_id，以 `expected_edit_revision` 指明编辑态，显示有效修订观察值；不新增审核角色或策略配置。
   - 现有 `ReviewSubmitModal`、`pluginReview.ts::submitPluginReview/publishPluginListing`、`skillApiReal::createReviewRequest/publishPlugin` 及全部/分类型发布按钮共同切换。Expert/Team 审核先取得已保存 editing 快照的精确 `version_id`，不再重传 content/relations，不在提交窗口 bumpPatch 出与快照不同的标签；需要新标签时先完成 CAS 保存再明确提交该返回版本。Skill 升级先把 `parse_task_id` 完整包保存为新草稿，再 A14 提交 `version_id`；MCP 的 `McpCreateModal` 审核模式先 A12 保存完整配置、拿 `edit_revision` 再 A14 提交。
   - ReviewQueue/ReviewDetailDrawer 及 `mapReviewRequest` 消费 A15 明确的 `frozen_version/frozen_connector`、`readme_content/frozen_relations` 和 `connector_observations`，按提交时 `baseline_version_id/dependency_diff` 展示新增/移除、目标版本变化、角色/Leader/顺序变化，不向 current 扇出补正文。Connector 用编辑修订及有效观察值，无业务 version；详情重字段不从队列列表臆造。批准只刷新 published/审核结果，不回写新 current；保留 Space 策略与 `useMyReviewState` 会话防陈旧更新。
8. 发布后回读并区分“草稿已保存/已提交审核/已发布/依赖仍待审核”；批准 v2 同时 current v3 时市场显示 v2、编辑保留 v3。`dependency_changed` 不自动更新目标重交，提示由有权取消者取消旧申请，再由有权提交者重新提交并生成新的 review_id；审核人查看新申请后批准。历史读取不回退线上：需要内容回退时从旧版本复制内容生成新草稿并走正常发布/审核，不直接切 published。
9. 保留 Web/CLI 原 `POST /plugins/install`，body 保持 `plugin_id/workspace_id/runtime_id/custom_env?`，CLI schema 补齐已有 `custom_env`；新增必填 `Idempotency-Key`，用户身份、Space 上下文、runtime UUID 按 02 校验。结果仍为 `{agent_id}` 或 `{squad_id}`；不新增 bindings 参数，不把 Fleet 内部 ID 命名外泄。
10. 每次明确安装意图生成 key，同意图双击/网络重试复用原 key、原 plan 和完整规范化请求（含 workspace/runtime 及相同运行时 Secret）；CLI 暴露显式 key，不自动为重试生成新值。仅在现有窗口内允许安全重试，Secret 由用户重新提供且须与原请求相同，不落本地、Marketplace、日志、命令回显或恢复文件。Web 仅保存无 Secret 的防重复与人工核对元数据；重启后无法重建原参数时不得自动重发，不承诺无凭据找回结果。已有本地 attempt 的已知成功结果可直接回放；结果未知、超窗、输入不同或无法重建时保留未知并人工核对，不能换 key、删记录、切 URL 或 fallback 安装。
   - **配置/依赖变化（D9）：**同 key 不同输入/配置 409 `idempotency_input_mismatch`，不得自动重解析后重发；依赖目标在首次 plan 后发新版不会改变原 key（原 plan 继续有效）；冻结节点被删除/下架时 409 `dependency_changed`。UI 提供“重新安装（新 key，最新配置）”，点击后生成新 key/新 attempt，按最新解析安装；不能把新 key 的 409 静默重试成旧 key。
   - Web 入口为 `ExpertAddToLoopModal`→`expertService::{installExpertToLoop,installSquadToLoop}`。key 和原参数的意图所有权需放在单次 modal 生命周期之外：关闭/重开、组件卸载、账号/Space 切换均不得把未知意图当新安装；稳定 key、无 Secret 元数据和已知结果按原用户/服务源/Space/workspace/root 隔离。adapter 保留 02 安全的 `attempt_id/retry_until/can_retry_same_key` 和 reason，不能把 `installation_in_progress`、`expired`、`input_mismatch` 或 unknown 统统归为“同名冲突”。
   - CLI 现有 `plugin.install` 为 `x-octo-retry:"never"`，继续保留明确人工重试语义与 `RESULT_UNKNOWN`。registry 声明 header 为必需并复用 `cmd/service/run.go::buildQueryAndHeaders`；成功响应缺 `agent_id/squad_id` 不能作为成功，未知错误保留 key 和安全 reason/截止时间，不能提示读 GET 找远端 receipt。
   - 安装仅允许权威用户身份。复用 `cmd/service/identity.go::applyIdentityRouting` 做可证明的格式预检时必须使用 `internal/credential/token.go` 真实枚举（user_key/user_bot/app_bot/loop_credential/unknown），不能写不存在的 `["user"]` 或把 user_bot 等同人类用户；token 前缀不替代服务端认证后的 User 判定。发布/读取/提交保留既有 User/Bot 范围与 owner 校验；A34 读取不因安装仅 User 而误禁 Bot。
   - `custom_env` 新增 schema 同时补真实脱敏：当前 `collectSecrets` 只收 promoted flags、不读取 `--data`，仅加 x-octo-secret 不会保护 map 值。沿已有 merged-body/writeOnly 边界处理 custom_env，确保 verbose/dry-run、transport/backend 错误和本地校验不输出任何值。CLI 关系语义校验接现有 JSON/schema 校验之后，固定目标 ID、Connector 禁版本、显式 relations、重复目标/成员 key/唯一 Leader/role/sort_order 与数量约束均本地定位字段，目标发布/受众/权限仍由服务端最终判定。
11. 删除过时提示、registry 描述与调用：保存即发布、遗漏关系会清空、已上架不能保存草稿、安装含旧 rollback、`mk1`/改名共存、依赖固定 pin 等叙述全部替换；同步已有 CLI 使用文档，不保留旧执行分支别名。

## 逐项验收（Acceptance）

- [ ] Web 生成的 Skill/MCP/Expert/Team Bot 提示能由真实 CLI 执行；依赖先发布、来源只引用目标 ID，不嵌入资源、不写 pin；错误停在对应步骤并保留脱敏证据。
- [ ] 用户确认、创建/更新、既有 private 发布、Space 提交/批准/拒绝/取消及回读闭环；自动审核开关开启/关闭行为和原角色权限不变。Bot 不自行绕过批准、不借用户身份安装；自动/人工审核都执行已确认的依赖矩阵。
- [ ] published v1 → 保存 v2 → 提审 v2 → 保存 v3 → 批准 v2：Web 市场/CLI 默认详情均 v2，编辑均 v3；内容、标签、文件和 relations 对齐。
- [ ] **B 跟随：** Skill/成员 Expert 发新版后，引用它的 Expert/Team 下一次读取/A34 展示最新内容；来源不需要发新版，Team 的成员角色/Leader/顺序不变；页面明确该依赖版本为“本次解析”，不把它宣称为固定。
- [ ] 一次 A34 GET 取得完整 Expert/Team 最新根、图、全部 `AGENTS.md`/`SKILL.md`/支持文件及有效 Connector，无依赖 detail/versions/skill_md/download/files 补请求，无 Fleet 调用或业务写入。A03 可独立展示图。
- [ ] 缺目标/文件、摘要错误、无权、未发布、跨 Space 及超预算均整体失败，无残缺 200、截断、缓存/latest/历史文件 fallback；`budget_exceeded=413`、文件完整性=500，`no-store`，市场原名/内容不变。
- [ ] CAS 冲突不覆盖；省略 relations 拒绝而非清空；只换目标/角色也可显示新保存版本；**内容回退只生成完整新草稿并正常发布，不改旧版本或直接切 published。**
- [ ] Web/CLI 旧安装 URL 均携带稳定 key，Bot 被拒；双击/丢响应重试仅一个资源结果，原键换参数 409，过期不自动新装，不跨 URL fallback。
- [ ] **新 key 最新（D9）：**配置或期望依赖变化时，UI 引导用新 key 重新安装并解析最新；同 key 不因依赖发新版而重解析。冻结节点删除/下架时 409 `dependency_changed`，不静默换版本。
- [ ] CLI help/registry/skill 与实际 wire 一致；错误输出不含 Token、custom_env、MCP 原文或虚构制品地址。
- [ ] 真实 Web 全部/专家/专家团 tab 均透传 A01 诊断并展示独立标记、hover/focus Tooltip 及多原因；正常无依赖显示 available，坏依赖不丢 mine 行/total，原发布/审核状态与操作不变，无前端过滤/依赖扇出/A34 取 tip。
- [ ] published 诊断不被新 editing 草稿覆盖；从未发布的草稿只称草稿依赖异常。跨 Space/无权/不可确认缺失原因仅匿名聚合，不从缓存或快照名泄露路径、身份、数量；依赖发新版/重上架随下一次查询重新解析。
- [ ] Web 独立 Skill ZIP 首次发布/已发布重传、MCP 完整表单编辑/审核、Expert/Team 既有 visibility 编辑、全部与分类型发布按钮、组织审核全部使用对应 CAS/指定快照协议；不再送 parse_task_id/content/relations 给 A14，不再由审核窗口任意重标版本。
- [ ] Space/账号切换清空诊断/图/文件/审核显示并取消旧读，缓存不跨身份或 Space；异步旧请求不更新新上下文。CLI 通过 `--data` 提供 custom_env 时 dry-run/verbose/校验及后端错误均不泄密，关系校验失败未发送请求。
- [ ] 无 `mk1`、frontmatter/BOM 改写、ZIP 补文件或同名共存提示；A34 返回原市场字节。

## 测试与可观测 smoke（Verification）

- 实施时扩展既有 prompt、API adapter、版本详情、审核 session guard、CLI registry/transport 测试，覆盖 CAS 字段、目标 ID、幂等头、未知结果、新 key 重装与过期；本设计阶段不运行检查。
- 实施时补充 `AllAssetsList` 的 `getMySkills` 跨类型 wire/domain/MineRow 映射、ExpertMarketListPage 专家/专家团共享映射、MineTable 状态并列与键盘 Tooltip、固定 reason 中英文和匿名隐私测试；真实 UI 与后端 COUNT/facets 联合证据由 REL-01 收口。
- 在隔离 Space 用 Web 现有 Bot 转发生成提示，以 Bot profile 在 CLI 发布 Skill/Connector/Expert/Team，并由有权用户完成 Space 审核；记录脱敏命令、资源/版本/review ID 和 Web/CLI 回读对照。
- 在上述已发布 Team、有效 Connector、另有未批准编辑的样本中，分别以 Web/CLI 抓取一次 A34 GET 并与清单逐路径/字节/hash 对照；再发布成员 Expert/Skill 新版，确认下一次 A34 跟随最新且 `bundle_digest` 变化。注入缺文件、错 hash、无权与 16MiB 越界，核对全失败、无补请求和零 Fleet 调用。
- 用用户 profile 和 Web 分别安装到真实 workspace/runtime；在 MP-03 的 POST 响应丢失注入下，窗口内以原 key、原 plan 和重新提供的相同 Secret 重传完整原请求，确认返回原 agent/squad 且计数不重增。再变更配置/期望依赖，确认同 key 409、新 key 新安装解析最新。重启后不能重建参数或超窗时只保留未知并人工核对。无真实 Fleet/Daemon 证据不得宣称安装 smoke 通过。
- **不运行 A29/A30 HTTP 或同名共存用例**；它们不在范围。

## 部署前置

- MP-01/MP-02 先提供正式版本、published 图、四字段诊断、CAS 及接入既有角色/Space 策略的新审核 DTO；现有权限与上下架检查继续生效，不增加新治理入口。MP-03 随后提供 A34 最新完整内容读取、统一安装与原请求重试；Fleet 仅联调。CLI registry/skill 与 Web 提示同批切换，不引用尚不可用能力。客户端与后端数据表所有权未变；不部署 A29/A30。

### 依赖更新展示（本轮用户确认）

展示本次解析的依赖版本并按已有查询刷新；本期仅审计展示，不新增依赖更新主动通知或后台轮询。

## 本轮评审修订验收

- [ ] 409 dependency_changed 显示“依赖已更新，请由有权人员取消原申请并重新提交”；不循环 approve、不通过 GET 改旧观察值、不自动取消/代提。取消成功后由有权提交者重提原来源版本；新 review_id 展示新的依赖观察值并重新审核。
- [ ] Connector 重提先展示当前编辑内容并带当前 CAS；若原申请已批准则回读结果，不能继续取消/重提。按原角色展示取消与提交能力；迁移关联审核按 MP-04 blocked 规则处理，不承诺页面重提即可恢复迁移。
- [ ] 上游正常发布 v2 不阻断原 key 的 v1 重试；用户显式修改安装输入才使用新 key，冻结节点失效单独提示。Admin 不审核、依赖通知仅审计展示。

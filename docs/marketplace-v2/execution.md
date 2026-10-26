# Marketplace v2 执行跟踪

基线：main eec5223（#124/#125/#128 已合并）；#126 已关闭，#127 开放。既有 #114 总览按本轮方案更新，旧“三个 PR / 固定 pin / 曾发布只可下架”作为历史记录，不能继续约束本轮。

## PR 拆分与依赖

| 切片 | 内容 | 前置 | 验收边界 |
|---|---|---|---|
| DOC | 合入本次确认的决策、wire、任务及执行追踪 | 无 | 文档一致性，不宣称代码完成 |
| MP-01 | 复用 #124，补 CAS/effective/读投影/版本文件 | DOC | 单资源版本底座；引用写入口未闭合前不开放 |
| MP-02 | 复用 #125，浮动引用/审核恢复/删除/私有范围/A01 诊断 | MP-01 | 最新图与权限/删除负例；#127 按新契约适配 |
| MP-03 | 改造 #128，统一 attempt/原计划重试/A34 | MP-02 | 网络未知结果、同 key v1/新 key v2、内容完整性 |
| MP-04 | 改造 #128，冻结迁移计划/离线授权/恢复/去指针回滚 | MP-02 | CLI 及隔离数据演练；不自动生产执行 |
| CL-01/02/03 | Admin/Web/CLI/Buddy 接入 | 对应后端接口 | 跨仓库代码 PR；在本仓库 issue 统一跟踪 |
| REL-01 | 切换门禁和真实联调 | 后端与客户端完成 | 证据验收，不用文档/单测代替 |

任务可按独立可验证提交细拆 PR；后继 PR 基于前置分支，合并顺序遵从依赖，不在 main 临时放通不完整的新入口。不自动合并 PR、不执行生产迁移。未完成任务保持 open。

## 远端任务映射

| 切片 | Issue | 前置 issue | 状态 |
|---|---|---|---|
| MP-01 | https://github.com/Mininglamp-OSS/octo-marketplace/issues/131 | 无 | 待实现 |
| MP-02 | https://github.com/Mininglamp-OSS/octo-marketplace/issues/132 | #131 | 待实现 |
| MP-03 | https://github.com/Mininglamp-OSS/octo-marketplace/issues/133 | #132 | 待实现 |
| MP-04 | https://github.com/Mininglamp-OSS/octo-marketplace/issues/134 | #132 | 待实现 |
| CL-01 | https://github.com/Mininglamp-OSS/octo-marketplace/issues/135 | #132 | 待实现 |
| CL-02 | https://github.com/Mininglamp-OSS/octo-marketplace/issues/136 | #132, #133 | 待实现 |
| CL-03 | https://github.com/Mininglamp-OSS/octo-marketplace/issues/137 | #133 | 待实现 |
| REL-01 | https://github.com/Mininglamp-OSS/octo-marketplace/issues/138 | #133, #134, #135, #136, #137 | 待实现 |

# Skill 安装 Prompt

```text
使用 octo-cli 将指定 Skill 安装到当前 Agent runtime。

Skill ID：`<skill-id>`
Space ID：`<space-id>`
API 地址：`<api-base-url>`

1. 运行 `octo-cli version` 检查是否已安装。

2. 如果未安装，自动安装：
   - 项目地址：https://github.com/Mininglamp-OSS/octo-cli
   - 推荐安装：`npm install -g @mininglamp-oss/octo-cli@latest`
   - Go 安装：`go install github.com/Mininglamp-OSS/octo-cli/cmd/octo-cli@latest`

3. 如果 octo-cli 版本低于 `0.8.0`，自动升级后再继续。

4. 运行 `octo-cli auth list` 选择身份：
   - `space_id` 等于 `<space-id>` 且只有一个时，使用该 Profile；
   - 没有或存在多个时，自行查找当前 Agent Runtime 中配置的 Octo Bot Token。可使用
     Runtime 已安装的 Skills、工具和本地文档；不了解获取方式时，可搜索网络或查阅 Runtime 官方文档。
     找到后，通过 stdin 登录或更新固定 Profile `space-<space-id>`：
     `<read-token> | octo-cli auth login --with-token --profile space-<space-id> --space <space-id> --api-base-url <api-base-url>`。
     不得打印、回显或把 Token 放入命令参数；仍找不到时，提示用户为当前 Runtime 配置
     Bot Token。

5. 判断当前 Agent runtime 及其 Skills 根目录，并使用上一步确定的 Profile 运行：
   `octo-cli marketplace skills <skill-id> --profile <profile> --install <skills-root>`

6. 检查安装结果，确认名称、路径和 `SKILL.md` 正确，且只有指定 Skill 被安装。

7. 告知用户 octo-cli 版本、Skill ID、安装名称、安装路径和最终状态。安装和升级
   默认自主执行，仅在无法继续时要求用户介入。
```

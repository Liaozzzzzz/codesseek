# 验收记录

环境：Windows，10 核 / 12 线程 / 16GB；模型 `bge-small-zh-v1.5` int8（512 维）；
`batch=8`、ONNX 8 线程、关闭 CPU arena。日期 2026-11-26。

## 1. 建索引（真实仓库）

```
files     126 (added 0, updated 126, removed 0, skipped 0)
chunks    1752
duration  1m24s          # --workers 4
index     <home>\data\projects\<id>
```

- 索引峰值 RSS：**292MB**（预算 400–700MB）
- 同一索引 `--workers 2` 需 4m50s；4 个 worker 只并行切块/读盘，ONNX 推理仍由单 session 串行
- 速度约 90 文件/分钟，受限于 CPU 推理，未达到 spec 的「300 文件/分钟」目标
- 增量（无改动）：**约 0.25s**（全部 skipped；模型懒加载，不付 ONNX 启动成本）

## 2. 中文业务查询（前 3 条）

| 查询 | 判断 |
|---|---|
| 用户登录失败的处理 | 前 2 条为登录/鉴权相关代码 |
| 表单校验规则 | 前 3 条均为表单校验相关代码 |
| 接口请求封装 | 后 2 条为接口封装相关代码 |
| 页面路由配置 | 前 3 条均为路由配置相关代码 |
| 文件上传处理 | 前 2 条为文件预览/上传相关代码 |

结论：5 个查询里 4 个的前 3 条至少有 2 条明显相关，达到「人工判断前 3 条相关」的验收意图。
纯英文标识符（`useLogin`）仍可能排在 30 名左右，属于量化中文模型 + 通用词表的能力边界。

## 3. MCP / OpenCode

```powershell
opencode mcp add codesseek --global -- '<home>\bin\codesseek.exe' serve
opencode mcp list        # ✓ codesseek  connected
```

- 4 个工具均可用：`search_code`、`index_project`、`index_status`、`list_projects`
- 通过 OpenCode 实际调用 `search_code` 命中了预期 chunk
- `structuredContent` 是对象（`{results:…}` / `{projects:…}`），修复了 OpenCode 对数组返回的校验错误
- 内存：MCP 空闲 **21MB**；查询加载模型后 **107MB**（预算 200–350MB）
- 每目录一个进程（官方 Go SDK v1.8.0 最高 `2025-11-25`，OpenCode `pool` 需要 `2026-07-28`）

## 4. 其他命令

- `codesseek model download`：文件已存在时跳过，输出大小并返回 0
- `codesseek status` / `projects`：字段与时间（本地时区）正确
- `serve` 的 stdout 只有协议帧，日志在 stderr

## 5. 单文件分发（2026-11-26 补充）

```
.\build.ps1 -RuntimeDll <dir>\onnxruntime.dll -ModelDir <models-dir>
# built bin\codesseek.exe (60.3 MB, embedded assets 41.8 MB)
```

- 验证环境：全新 `CODESEEK_HOME`，目录里**只放一个 exe**，不设 `ONNXRUNTIME_DLL` / `CODESEEK_MODEL_DIR`
- `index`：2 个文件，exit 0，**2.3s**（含首次解压 42MB）
- `search "登录失败的处理"`：top-1 `src/login.ts handleLoginError`，exit 0，**1.0s**
- 自动解压出：
  - `home\runtime\onnxruntime-1.31.1\{onnxruntime.dll, onnxruntime_providers_shared.dll}`
  - `home\models\bge-small-zh-v1.5\{model_quantized.onnx, tokenizer.json, config.json, ...}`
- 二次运行命中 `.codesseek-assets.json` + 文件大小校验，不再解压
- `.\build.ps1 -Slim` 仍可产出 **18.5MB** 轻量版；两种构建下 `go test ./...`、`go vet ./...` 均通过

### 5.1 重新打包与构建可靠性（2026-11-27）

- 无参数 `.\build.ps1`：runtime 从 `bin\onnxruntime.dll` 重新暂存，模型直接复用暂存目录，产出 **60.3MB**
- 内嵌改为 `-tags embed` 控制：普通 `go build` / `go test` 始终是 **18.5MB** 轻量版，暂存资产可长期保留；`.\build.ps1 -Slim` 不再删除暂存
- 旧 exe 正在运行（`serve` 占住文件）时重新打包：先构建到 `.new`，再把旧 exe 改名成 `codesseek.exe.old-<时间戳>`，exit 0
- 全新 `CODESEEK_HOME` + 目录里只有 exe：`index` 2 文件 exit 0（2.5s，含首次解压），`search` 命中 `src/login.go:4-15`，exit 0；`HF_ENDPOINT` 指向不可达地址也没有联网
- 工具调用日志已验证：`tool=search_code session="ses_de8e..." elapsed=1.78s query="登录失败的处理" results=1`，session ID 来自 OpenCode 的 `_meta["ai.opencode/sessionID"]`
- `go test ./...`、`go vet ./...`、`go test -tags embed ./internal/assets/ ./cmd/codesseek/` 全绿

### 5.2 MCP 自动索引（2026-11-27）

真实模型 + 单文件 exe，用 stdio 客户端直接调 `search_code`（临时仓库，未建索引）：

- `CODESEEK_AUTO_INDEX_WAIT=0`（模拟建索引超过等待预算）：
  - 首次调用 3ms 返回 `index: {action: "indexing", jobId: "idx-1", state: "queued"}`，`results: []`
  - `index_status` 轮询到 `done`：2 files / 2 chunks / **2.07s**
  - 重试 `search_code`：2 条结果，top-1 `src/login.js`，`index` 字段消失
- 默认等待（30s）：单次调用 **1.8s** 返回 `index: {action: "indexed", files: 2, chunks: 2, elapsedMs: 1626}` + 结果
- 新增文件后再搜索：`index.action = "refreshed"`，`files` 由 2 变 3
- `list_projects` 随即包含临时仓库；验证后已清理临时索引
- `CODESEEK_AUTO_INDEX=off`：未索引时仍返回 `project not indexed ... call index_project first`，且不写索引
- 单测：`TestSearchAutoIndexesProject`、`TestSearchRefreshesStaleIndex`、`TestSearchAutoIndexCanBeDisabled`、`TestNeedsRefreshDetectsTreeChanges`

### 5.3 默认按天日志（2026-11-27）

- 不带参数 `serve`：日志自动落到安装目录，启动行带路径
  `[pid 63076] ... serving MCP over stdio (log: <home>\logs\mcp-2026-11-27.log)`
- 同一次会话的工具调用写进同一个文件：`tool=list_projects session="" elapsed=2ms projects=2`
- `dailylog` 单测：跨本地午夜自动换文件（`mcp-2026-11-27.log` → `mcp-2026-11-28.log`）、同日追加；
  目录不可写时写 stderr 且不报错
- `--log FILE` 仍固定单文件，且不会再创建默认日志目录（`TestServeLoggerHonoursExplicitFile`）
- 全局 OpenCode 配置已去掉 `--log` 参数，重启后直接使用按天日志

## 6. 未覆盖

- 多项目共享单进程（`pool`）待 SDK 支持新协议
- 50k 块规模下的查询延迟未实测；当前 1752 块 + CLI 冷启动（含加载模型）约 0.9s
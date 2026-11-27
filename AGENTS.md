# AGENTS.md — 给接手这个仓库的 Agent

## 这是什么

`codesseek`：本地代码语义检索工具。Go 单文件分发（可选内嵌 ONNX Runtime + 模型），CLI + MCP Server，ONNX int8 小模型（`bge-small-zh-v1.5`，512 维），目标是「任意本地仓库都能索引、能按中文意思搜代码」，并且内存占用远低于 Python 方案。

完整设计（数据格式、切块规则、检索重排、MCP 工具签名、验收标准）在 [`docs/spec.md`](docs/spec.md)，使用说明在 [`README.md`](README.md)。

**当前状态：核心功能已实现并跑通。** CLI（`index/search/status/projects/version`）、MCP（`serve`，4 个工具）、`model download` 均可用；单元测试 + `-tags modeltest` 全绿。单文件 exe 已实测：目录里只有 exe 时首次索引自动解压 runtime + 模型并离线检索。

## 接手先看什么

1. `docs/spec.md` 第 5–10 节（存储/切块/嵌入/重排/MCP）
2. `internal/searcher/searcher.go`（重排）、`internal/terms/terms.go`（中英词表）、`internal/indexer/indexer.go`（`embedText` 与 `ConfigHash`）
3. `cmd/codesseek/serve.go`（MCP 工具实现与 root 解析）

## 已定决策（不要反复推翻）

| 决策 | 结论 | 原因 |
|---|---|---|
| 语言 | Go | 单文件分发、无 Python/torch；官方 MCP SDK 可多项目共享进程 |
| 分发 | `build.ps1` 把 ONNX Runtime + 默认模型暂存后，用 `go build -tags embed` 嵌进 exe（约 60MB），首次使用时解压到 home | 一个 exe 离线可用；大二进制不进 git，构建时从本地暂存；无 `embed` 标签时永远是轻量版 |
| 模型 | `bge-small-zh-v1.5` ONNX int8 | 512 维、24MB、中文强；别换 `bge-m3`（内存翻数倍） |
| 切块 | 语言感知行窗口，不用 tree-sitter | 避免 CGO 编译风险；首版够用 |
| 存储 | 自定义 `vectors.bin` + `jsonl` | 纯 Go、无 SQLite/CGO；顺序读盘够快 |
| 原文 | 只存行号，检索时回读 | 省磁盘；配合 `stale` 标记处理文件变更 |
| 并发 | workers 默认 2（大仓库可 `--workers 4`），batch 8，ONNX 线程 `min(8, NumCPU-1)`，关 CPU arena | 压住内存峰值（实测 292MB）同时避免 2 线程推理过慢 |
| 中文桥接 | 查询扩展 + 代码块 `keywords` 行 | 量化中文模型对英文标识符弱；词表是通用编程词，不含具体仓库业务词 |
| MCP | 官方 SDK + stdio | SDK v1.8.0 最高协商 2025-11-25，OpenCode 的 `pool` 暂不可用 |
| 自动索引 | `search_code` 缺索引/过期时先增量建 + 刷，默认同步等 30s，超时转后台任务；`CODESEEK_AUTO_INDEX=off` 可关 | Agent 不必先手动 `index`，用户提问即可；等待预算与安全边界（显式 root 或 cwd 有项目标记）压住误索引风险 |

## 硬约束

- **stdout 洁净**：`serve` 模式下 stdout 只输出 MCP 协议帧；日志默认写 `<home>/logs/mcp-YYYY-MM-DD.log`（按天切），`--log FILE` 可固定文件；任何情况都不许写 stdout
- **离线**：运行期不联网；只有 `model download` 允许联网且必须支持 `HF_ENDPOINT`
- **不 panic**：工具错误用结构化错误返回；单文件失败只跳过
- **不混用向量**：切块参数/模型/嵌入模板变更后必须 `--full`（由 `ConfigHash` 拦截）
- **MCP 返回**：`structuredContent` 必须是 JSON 对象；列表结果包一层 `{results:…}` / `{projects:…}`（OpenCode 会校验）

## 常用命令

```powershell
# 轻量版（约 18MB，需要同目录 onnxruntime.dll + 模型）
go build -o bin\codesseek.exe .\cmd\codesseek

# 单文件版（内嵌 runtime + 模型，约 60MB；暂存过资产后可省略参数）
.\build.ps1
.\build.ps1 -RuntimeDll <dir>\onnxruntime.dll -ModelDir <models-dir>
.\build.ps1 -Slim            # 轻量版；不再删除暂存资产

go test ./...
go vet ./...
go test -tags embed ./internal/assets/ ./cmd/codesseek/   # 覆盖内嵌解压逻辑

# 真实模型测试（较慢）
$env:CODESEEK_MODEL_DIR='<models-dir>'
$env:ONNXRUNTIME_DLL='<path>\onnxruntime.dll'
go test -tags modeltest ./internal/indexer/ ./internal/embedder/

# 验收
.\bin\codesseek.exe index <repo>
.\bin\codesseek.exe search "用户登录失败的处理" --path <repo>
```

安装布局：`<home>\bin\codesseek.exe`（单文件版内嵌 runtime + 模型，约 60MB）；首次真正用到模型时解压到 `<home>\{runtime,models}`，索引落在 `<home>\data`。已存在的 DLL/模型优先于内嵌副本。内嵌只在 `-tags embed` 构建时存在，普通 `go build` 产物约 18MB。

## 注意事项

- PowerShell 控制台默认 GBK，打印/重定向中文可能乱码；调试时先 `chcp 65001` 或用 `--json` + UTF-8 读取
- 不要用 `Get-Content`/`Set-Content` 往返编辑含中文的 Go 文件（会破坏 UTF-8），用编辑工具
- 目标仓库源码可能是 GBK：读取必须走 `internal/textenc.Decode`
- Windows 下正在运行的 exe 不能覆盖：`build.ps1` 已自动处理（先构建到 `.new`，再把旧 exe 改名成 `codesseek.exe.old-<时间戳>`）；手工替换时也要先 `Move-Item` 改名
- 单文件版约 60MB；`internal/assets/data/{runtime,model}` 是构建时暂存的大文件，已 gitignore，别提交；暂存可长期保留，`-tags embed` 之外不会被打进产物
- 单文件版的 home 默认由 exe 位置决定（`bin/` 的父目录），否则是 `~\.codesseek`；可用 `CODESEEK_HOME` 指定
- `serve` 每次工具调用写 `tool=... session="ses_..." elapsed=...` 日志，默认按天落 `<home>\logs\mcp-YYYY-MM-DD.log`（`--log FILE` 可固定文件）；排查“Agent 到底用没用检索”先看这个，`opencode mcp list` 只代表连接成功
- `search_code` 自动建/刷索引（默认同步等 30s，超时转后台任务；`CODESEEK_AUTO_INDEX=off` 可关）；只有显式 `root` 或 cwd 带项目标记才自动建，磁盘根 / home 会拒绝
- 升级 exe 后必须重启 OpenCode（或新开会话），已连接的 MCP 进程不会换成新二进制；`serve` 进程被 kill 后当前会话的工具目录会消失
- 首解压只在真正加载模型时发生（懒加载），无改动的增量索引不会解压 60MB；目标目录带 `.codesseek-assets.json` 标记
- 索引很吃 CPU：全量索引约 1.5 分钟（`--workers 4`）；增量无变化约 0.25 秒
- `docs/spec.md` 与实现如有出入，以代码 + README 为准，并顺手修正 spec

## 已知限制 / 待办

- 官方 Go MCP SDK 支持 2026-07-28 后可开启 OpenCode `pool`，实现真正的多项目单进程
- 量化模型对未收录缩写的召回一般；扩充词表时保持「通用编程词」，不要写死业务词
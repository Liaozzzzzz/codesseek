# codesseek

本地代码语义检索（semantic grep）：给电脑上的任意仓库建一份向量索引，然后按**意思**找代码，而不是按字面找。

- **全本地**：ONNX int8 小模型（`bge-small-zh-v1.5`，512 维，约 23MB），运行期不联网、不调 API、不依赖 Python/torch
- **单文件分发**：`build.ps1` 可把 ONNX Runtime 和模型一起嵌进 exe（约 60MB），拷一个文件就能离线运行；`-Slim` 仍是 18MB 轻量版
- **省内存**：MCP 空闲约 20MB，首次查询加载模型后约 110MB；索引峰值约 300MB（workers=2）
- **中文友好**：中文注释、中文查询都能命中；查询和代码块两侧都有中英编程词表做桥接

```powershell
codesseek index "D:\workspace\some-repo"          # 建索引（增量）
codesseek search "用户登录失败的处理" --path "D:\workspace\some-repo"
codesseek serve                                   # 作为 MCP Server 供 Agent 调用
```

设计与验收标准见 [`docs/spec.md`](docs/spec.md)。

## 安装

单文件（推荐，离线自包含，约 60MB）：

```powershell
.\build.ps1                      # 自动在 bin\、CODESEEK_HOME、~\.codesseek 下找 DLL/模型
.\build.ps1 -RuntimeDll D:\programs\codesseek\bin\onnxruntime.dll `
            -ModelDir   D:\programs\codesseek\models   # 或显式指定
```

脚本先把 runtime 与模型暂存到 `internal\assets\data\`，再用 `go build -tags embed` 编进 exe；
产物是 `bin\codesseek.exe`，不依赖旁边的 DLL 或 `models\` 目录。暂存过一次以后，直接
`.\build.ps1` 就会复用暂存文件，不必再带参数。首次真正用到模型时，程序自动把
runtime 解压到 `<home>\runtime\onnxruntime-1.31.1\`、模型解压到 `<home>\models\bge-small-zh-v1.5\`，
之后复用（带 `.codesseek-assets.json` 版本标记，不会重复解压）。

exe 正在运行（例如 OpenCode 的 MCP 进程）也能重新打包：脚本先构建到临时文件，旧 exe
会被改名成 `codesseek.exe.old-<时间戳>` 再替换，重启 MCP 进程即可用上新版本。

轻量版（运行时仍需 exe 同目录的 `onnxruntime.dll` 和模型）：

```powershell
$env:GOCACHE    = 'D:\programs\go-build'
$env:GOMODCACHE = 'D:\.go-store'
$env:GOPROXY    = 'https://goproxy.cn,direct'      # proxy.golang.org 在部分网络不可达
go build -o bin\codesseek.exe .\cmd\codesseek      # 约 18MB，永远不内嵌
# 或： .\build.ps1 -Slim
```

内嵌资产只在 `-tags embed` 时参与编译，所以暂存的 42MB 文件可以一直留在
`internal\assets\data\`，不会把日常 `go build` / `go test` 意外变成 60MB；`-Slim` 也不再删除暂存。

Windows 的轻量版需要把 `onnxruntime.dll`（1.31.1，与 `onnxruntime_go v1.38.0` 匹配）放在二进制同目录。

把 `codesseek.exe` 放到 `D:\programs\codesseek\bin\` 后，程序会自动把
`D:\programs\codesseek\models` 和 `D:\programs\codesseek\data` 当作默认模型/索引目录，
无需任何环境变量。已存在的 DLL/模型优先于内嵌副本，便于手动升级或换模型。

## 下载模型

单文件版通常不需要这一步；轻量版或想换模型时使用：

```powershell
$env:HF_ENDPOINT = 'https://hf-mirror.com'          # huggingface.co 不通时使用
codesseek model download                            # 已有文件会跳过；--force 强制重下
```

文件来自 `Xenova/bge-small-zh-v1.5`：`onnx/model_quantized.onnx`、`tokenizer.json`、`config.json`。

## 建索引与检索

```powershell
codesseek index "D:\repo"                    # 增量；模型/切块配置变化时会要求 --full
codesseek index "D:\repo" --full             # 全量重算
codesseek search "登录失败时怎么重试" --path "D:\repo" --top 5
codesseek search "login retry" --path "D:\repo" --json
codesseek status "D:\repo"
codesseek projects
```

- 源码按 UTF-8 读取，非法 UTF-8 自动回退 GB18030（兼容 GBK 仓库）
- 索引只存行号与向量，检索时回读源码；文件被改过或索引超过 24h 会带 `stale: true`（MCP `search_code` 会先自动增量刷新）
- 无改动的重复索引会跳过 ONNX 加载（约 0.25s）；只有真有文件变化时才加载模型
- `--json` 输出 `[{path, startLine, endLine, score, symbol, snippet, stale}]`
- 返回码：`0` 成功、`1` 运行错误、`2` 用法错误、`3` 项目未索引

## 接入 OpenCode（MCP）

推荐用 CLI 注册，避免手改配置：

```powershell
opencode mcp add codesseek --global -- 'D:\programs\codesseek\bin\codesseek.exe' serve
opencode mcp list
```

等价的全局配置（`~/.config/opencode/opencode.json`，OpenCode V2）：

```jsonc
{
  "mcp": {
    "servers": {
      "codesseek": {
        "type": "local",
        "command": ["D:\\programs\\codesseek\\bin\\codesseek.exe", "serve"],
        "environment": { "CODESEEK_HOME": "D:\\programs\\codesseek" }
      }
    }
  }
}
```

工具：

| 工具 | 入参 | 返回 |
|---|---|---|
| `search_code` | `query`(必填), `root?`, `topK?`, `pathPrefix?` | `{results: [{path, startLine, endLine, score, symbol, snippet, stale}], index?}` |
| `index_project` | `root?`, `full?` | `{jobId, root, startedAt}`，后台执行 |
| `index_status` | `root?`, `jobId?` | `{state, processed, total, files, chunks, elapsedMs, error?}` |
| `list_projects` | — | `{projects: [{root, files, chunks, indexedAt, model, dim}]}` |

`root` 解析顺序：显式入参 → 从当前目录向上匹配已索引项目 → 当前目录含项目标记（`.git`/`go.mod`/`package.json`…）→ 唯一已索引项目 → 报错并列出候选。
`serve` 的 stdout 只输出 MCP 协议帧；日志**默认按天落到安装目录**：`<home>\logs\mcp-YYYY-MM-DD.log`（`--log FILE` 可改成固定文件）。

### 自动索引（Agent 不用先手动 index）

`search_code` 会自己把索引准备好，不需要用户先跑 `codesseek index`：

- 项目没索引 → 先建索引再检索，返回里带 `index: {action: "indexed", files, chunks, elapsedMs}`
- 索引里记录的文件有增删改，或索引超过 24h → 先做一次增量刷新，`action: "refreshed"`
- 首次建索引超过等待预算（默认 30s）→ 立即返回 `action: "indexing"` + `jobId`，
  Agent 按 Instructions 轮询 `index_status` 直到 `done`，再重试 `search_code`
- 索引已是最新 → 不返回 `index` 字段，也没有额外开销（检查按 10s 缓存）

安全边界：只有显式 `root` 或**当前目录带项目标记**时才会自动建索引；磁盘根目录、用户 home 目录会被拒绝，避免 Agent 误索引整个盘。`root` 参数也支持用户直接点名某个仓库。

配置：

```powershell
$env:CODESEEK_AUTO_INDEX = 'off'   # 关闭自动索引，恢复 index_project 合同
$env:CODESEEK_AUTO_INDEX_WAIT = '60'  # 首次建索引的同步等待秒数（默认 30）
```

> 升级 exe 后要重启 OpenCode（或新开一个会话）才会换上新的 MCP 进程；已连接的旧进程不会自动换二进制。

### 怎么确认 Agent 真的用了检索

OpenCode 默认开启 Code Mode，模型是在 `execute` 里调用 `tools.codesseek.search_code(...)`，
所以会话里看到的是 `execute` 调用；`opencode mcp list` 显示 connected 只说明“可用”，不代表“用了”。

`serve` 默认把每次工具调用写一行到安装目录（含触发它的 OpenCode session ID）：

```
<home>\logs\mcp-2026-11-27.log        # 跨天自动换文件，无需配置
```

```jsonc
// 默认就是这样，不用加参数
"command": ["D:\\programs\\codesseek\\bin\\codesseek.exe", "serve"]
```

```
[pid 39752] 2026-11-27 09:29:58 tool=search_code session="ses_de8e..." elapsed=1.78s query="登录失败的处理" root="..." results=1 index=refreshed
```

- 今天的日志：`Get-Content "D:\programs\codesseek\logs\mcp-$(Get-Date -Format yyyy-MM-dd).log" -Tail 30`
- 只查检索：`Select-String 'tool=search_code' D:\programs\codesseek\logs\*.log`
- 实时看：`Get-Content "D:\programs\codesseek\logs\mcp-$(Get-Date -Format yyyy-MM-dd).log" -Tail 20 -Wait`
- 没有任何 `tool=` 行，说明这次会话没走检索（模型可能直接用了 grep）
- 多个目录/进程共用当天日志时，用行首 `[pid N]` 区分
- 想固定到一个文件（不按天分）：`serve --log D:\programs\codesseek\logs\mcp.log`

> `opencode mcp add ... -- <exe> serve --log <file>` 会把 `--log` 当成 CLI 自己的参数
> （报 `Unrecognized flag: --log`），需要手工写进配置的 `command` 数组；默认按天日志不需要任何参数。

> 协议兼容性：官方 Go SDK v1.8.0 最高协商 `2025-11-25`。OpenCode 的 `pool: true`
> 只在 `2026-07-28` 连接间共享，因此当前每个工作目录各起一个 `codesseek.exe`；
> 空闲进程约 20MB，暂不构成问题。等 SDK 支持新协议后可开启 `pool`。

## 检索是怎么做准的

1. **切块**：按语言（Vue/TS/Python/Markdown…）切行窗口，保留 `kind` 与顶层符号名
2. **嵌入文本**：`路径 + 类型/符号 + keywords 行 + 代码`；`keywords` 由通用中英编程词表从英文标识符反推（`login`→登录、`token`→令牌），只参与向量化，不影响返回片段
3. **查询扩展**：中文查询补英文代码词（`登录`→`login`），英文查询补中文词，弥补中文模型对英文标识符的弱项
4. **重排**：`0.75*cosine + 0.15*路径命中 + 0.10*代码命中`，同文件最多 2 条，超出降权

## 配置

优先级：命令行参数 > 环境变量 > 项目 `.codesseek/config.json` > 全局 `~/.config/codesseek/config.json` > 默认值。

| 变量 | 说明 |
|---|---|
| `CODESEEK_HOME` | 模型与索引根目录（默认：`bin/` 的父目录，否则 `~/.codesseek`） |
| `CODESEEK_MODEL_DIR` | 模型目录 |
| `CODESEEK_DATA_DIR` | 索引目录 |
| `CODESEEK_MODEL` | 模型标识，默认 `BAAI/bge-small-zh-v1.5` |
| `CODESEEK_WORKERS` | 索引并发，默认 2 |
| `ONNXRUNTIME_DLL` | 指定 DLL 路径（优先级高于内嵌解压） |
| `HF_ENDPOINT` | 仅 `model download` 使用 |

## 开发

```powershell
go test ./...
go vet ./...

# 内嵌解压测试（需要先暂存资产，会实际解压约 42MB）
go test -tags embed ./internal/assets/ ./cmd/codesseek/

# 真实模型集成测试（会加载 ONNX，较慢）
$env:CODESEEK_MODEL_DIR = 'D:\programs\codesseek\models'
$env:ONNXRUNTIME_DLL   = 'D:\workspace\open-source\codesseek\bin\onnxruntime.dll'
go test -tags modeltest ./internal/indexer/ ./internal/embedder/
```

目录：

```
cmd/codesseek/        入口：index / search / status / projects / serve / model
internal/config/      配置加载与默认值
internal/scanner/     文件扫描：gitignore + 扩展名白名单 + 大小/二进制过滤
internal/chunker/     语言感知切块
internal/textenc/     UTF-8 / GB18030 解码
internal/embedder/    tokenizer + onnxruntime + CLS pooling + L2 normalize
internal/terms/       中英编程词表：查询扩展 + 代码块中文关键词
internal/store/       meta / files / chunks / vectors 读写，原子替换
internal/indexer/     增量 diff 与索引任务
internal/searcher/    余弦 Top-K + 重排 + 片段回读
```

## 已知限制

- 量化版 bge-small-zh 对纯英文标识符的理解有限，靠词表桥接；非常见业务词（未收录的缩写）可能召回一般
- 索引期是 CPU 密集型：126 文件 / 1752 块约 1.5 分钟（`--workers 4` + 自动 8 线程）；workers=2 时约 5 分钟
- 详见 [`docs/acceptance.md`](docs/acceptance.md)
- 查询会把整个 `vectors.bin` 顺序读一遍；5 万块约 100MB，仍在百毫秒级
- 切换模型、切块参数或嵌入模板后必须 `index --full`，程序会拒绝混用旧向量
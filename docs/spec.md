# codesseek 设计文档

> 状态：设计定稿，待实现
> 最后更新：2026-11-26

## 1. 背景与目标

在编码环境里需要一个「按语义找代码」的能力，覆盖**任意本地仓库**，而不只是单个工作区。

已有的现成方案都不满足要求：

| 方案 | 问题 |
|---|---|
| VectorCode (Python) | 每个项目一份 MCP 进程 + ChromaDB 子进程，空闲 0.4–0.6GB，查询后 0.8–1.2GB，多开成倍涨 |
| claude-context | 依赖 Docker + Ollama，环境太重 |
| dfine-semantic-mcp | 固定英文模型、模型地址写死 huggingface.co（本机不可达） |

因此自研一个 Go 薄壳：单二进制、无 Python、无服务端、MCP 可共享进程。

**目标**

1. 任意本地目录：`codesseek index <path>` 后即可检索，索引按项目隔离、集中存放
2. 中文查询可用：中文注释、中文业务词能召回正确代码块
3. 低占用：空闲 ≤ 80MB，查询后 ≤ 400MB，多项目共用一份 MCP 进程
4. 增量：文件 sha256 未变则跳过；变更文件只重算自身
5. 全离线运行：只有首次下载模型需要网络，且可走 `hf-mirror.com`

**非目标（首版不做）**

- 文件监听 / 自动重建索引（用 `stale` 标记 + 主动调用代替）
- AST 级 tree-sitter 解析（用语言感知的行窗口 + 大括号/缩进识别代替，避免 CGO 编译风险）
- 全文关键词检索（BM25）、跨仓库联合检索
- 多模态、代码补全、生成

## 2. 总体架构

```
CLI (cobra-free, stdlib flag)          MCP stdio
        │                                   │
        ├── indexer ── scanner ── chunker ──┤
        │        │                          │
        │        └── embedder (ONNX)        │
        │                 │                 │
        └────────── store (mmap/read) ──────┘
                          │
                  searcher (cosine + rerank)
```

| 模块 | 职责 |
|---|---|
| `internal/config` | 读取 `~/.config/codesseek/config.json`，合并默认值与 CLI 覆盖 |
| `internal/assets` | 内嵌 ONNX Runtime 与默认模型（单文件分发）；首次使用时原子解压，带版本标记 |
| `internal/scanner` | 遍历仓库；解析 `.gitignore`、`codesseek.ignore`；扩展名白名单；跳过 >1MB 文件与二进制 |
| `internal/chunker` | 按语言切块，产出 `path / startLine / endLine / kind / symbol / text` |
| `internal/embedder` | 加载 tokenizer.json + ONNX 模型；批处理编码；CLS pooling + L2 归一化 |
| `internal/store` | 项目级目录：meta/files/chunks/vectors；临时文件 + rename 原子提交；`lock` 文件防并发写 |
| `internal/indexer` | 扫描 → 切块 → 与旧 files 对比 → 只嵌入变化块 → 删除孤儿块 → 更新 meta |
| `internal/searcher` | 全量点积 Top-K → 重排 → 回读源码片段 |
| `cmd/codesseek/serve.go` | 官方 SDK，暴露 4 个工具；stdout 只输出协议帧 |

## 3. 目录与数据格式

### 3.1 全局布局（默认值，可用配置覆盖）

```
<home>\
  bin\codesseek.exe                 # 单文件版内嵌 runtime + 模型（约 60MB）
  runtime\onnxruntime-1.31.1\       # 单文件版首次使用时自动解压
      onnxruntime.dll
      onnxruntime_providers_shared.dll
      .codesseek-assets.json
  models\bge-small-zh-v1.5\
      model_quantized.onnx   (~24MB)
      tokenizer.json
      config.json
      .codesseek-assets.json
  data\projects\<sha1(realpath)>\
      meta.json
      files.jsonl
      chunks.jsonl
      vectors.bin
      lock
  logs\mcp-YYYY-MM-DD.log

%USERPROFILE%\.config\codesseek\config.json
```

`data` / `models` 可配置到任意可写目录。

单文件版（`build.ps1`，编译时带 `-tags embed`）把 runtime 与模型嵌进 exe；首次真正需要模型时解压到上面的
`runtime\` / `models\`，已有文件优先，因此旧的「exe + onnxruntime.dll + models」布局仍可用。
轻量版（普通 `go build` 或 `build.ps1 -Slim`，无 `embed` 标签）不内嵌任何大文件，要求这两份文件已经在磁盘上。

### 3.2 meta.json

```json
{
  "version": 1,
  "root": "<project-path>",
  "model": "BAAI/bge-small-zh-v1.5",
  "dim": 512,
  "chunkConfigHash": "sha256:...",
  "fileCount": 123,
  "chunkCount": 1874,
  "indexedAt": "2026-11-26T10:00:00Z",
  "durationMs": 42000
}
```

`chunkConfigHash` 覆盖切块参数 + 嵌入文本模板 + 模型名。不一致时拒绝增量，提示 `--full`，绝不混用两套向量。

### 3.3 files.jsonl

每行一条，按路径升序：

```json
{"path":"src/pages/login.vue","size":4821,"mtime":1764000000,"sha256":"ab12...","chunkStart":0,"chunkCount":12}
```

`chunkStart/chunkCount` 指向 `chunks.jsonl` 的连续区间，查询命中后按 chunk 索引反查文件记录。

### 3.4 chunks.jsonl

```json
{"path":"src/pages/login.vue","startLine":120,"endLine":178,"kind":"function","symbol":"handleSubmit","sha256":"9f3c..."}
```

不存原文，节省空间；检索命中后按 `startLine/endLine` 回读文件。若文件当前 sha256 与 files 记录不符，标记 `stale: true`。

### 3.5 vectors.bin

连续 float32，行主序，`chunkCount × dim`，全部 L2 归一化。读取时按 offset 直接切片，不整体加载（`os.File.ReadAt`）。

## 4. 配置

`~/.config/codesseek/config.json`（全部字段可省略）：

```json
{
  "model": "BAAI/bge-small-zh-v1.5",
  "modelDir": "<home>/models",
  "dataDir": "<home>/data",
  "chunkSize": 1000,
  "overlapRatio": 0.15,
  "maxFileBytes": 1048576,
  "include": [".vue", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".java", ".kt", ".py", ".go", ".rs", ".cs", ".sql", ".md", ".json", ".yaml", ".yml"],
  "excludeDirs": ["node_modules", "dist", "build", ".nuxt", ".output", ".git", "vendor", "target", "__pycache__", ".venv"],
  "excludeGlobs": ["*.min.js", "*.map", "*.lock", "pnpm-lock.yaml", "package-lock.json", "*.snap"],
  "workers": 2,
  "nResult": 8
}
```

优先级：CLI flag > 项目内 `.codesseek/config.json` > 全局配置 > 内置默认值。

`workers` 默认 2（不是 `NumCPU`），控制索引期内存峰值。

## 5. 索引流程

```
resolveRoot → loadConfig → scan(files) → diff(旧 files.jsonl)
  ├─ 未变文件：复用 chunkStart 区间，不重新切块/嵌入
  ├─ 变更/新增：重新切块 → 批量嵌入 → 追加
  └─ 删除：标记为孤儿
→ 压缩重写 files/chunks/vectors（临时文件 + rename）
→ 删除孤儿块 → 更新 meta.json
```

要点：

- 扫描阶段先算 sha256，再决定是否切块（省掉大量 IO）
- 嵌入批大小按 token 总数控制（单批 ≤ 8192 token），避免 OOM
- 任意单文件失败（解码失败、tokenizer 异常）只记 warning 并跳过，不中断整批
- 进度写 stderr（CLI 可关）；MCP 侧写入任务状态，供 `index_status` 查询
- 项目级 `lock` 文件：索引进行中再次 index 直接返回「已有任务运行中」

## 6. 切块策略

| 语言 | 规则 |
|---|---|
| Vue / SFC | 先按 `<template>` `<script>` `<style>` 分段，script 内再按函数/类切，template 按顶层标签块 + 行窗口 |
| TS/JS/TSX/Java/C#/Go/Rust/Kotlin | 括号深度跟踪：在深度 0 处识别 `func/function/class/interface/enum/const/def/struct` 起始，块过长（>chunkSize）再按行切 |
| Python | 缩进识别顶层 `def` / `class`，同级方法归入类块 |
| Markdown | 按 `#` 标题分段，超长按段落切 |
| SQL | 按 `;` 语句切，超长按行窗口 |
| 其他 | 60 行窗口 + 15% 重叠 |

统一约束：

- 单块软上限 `chunkSize`（默认 1000 字符），硬上限 1.5×
- 重叠只在「行窗口」切分时产生
- 嵌入文本 = `path + "\n" + kind + " " + symbol + "\n" + ["keywords: <中文概念>…" + "\n"] + 代码`
- `keywords` 行由 `internal/terms` 的通用编程词表从英文标识符反推（如 `login`→登录、`token`→令牌），只参与向量化，不入库、不影响返回片段
- 查询侧使用同一词表做中英扩展（如「登录」→`login`），弥补中文模型对英文标识符的跨语言弱项
- 块行号 1-based，`endLine` 含

## 7. 向量化细节

- 模型：`BAAI/bge-small-zh-v1.5` 的 ONNX int8 版本（`Xenova/bge-small-zh-v1.5` 转换产物），512 维
- tokenizer：`tokenizer.json`（WordPiece + BertNormalizer + BertPreTokenizer + TemplateProcessing，vocab 21128 + 5 个 added token），用 `github.com/sugarme/tokenizer`
- ONNX 运行时：`github.com/yalue/onnxruntime_go` + `onnxruntime.dll`（从 PyPI `onnxruntime` wheel 提取，约 16MB，无 Python 依赖）
- 输入：`input_ids`、`attention_mask`、`token_type_ids`（int64，`[1, seq]`）；`maxLen=512`，按 attention_mask 做 mean-ignore 的 CLS pooling
- 输出：取 `last_hidden_state[:, 0, :]`（以模型实际输出名为准）
- 归一化：L2，入库前与查询前都做
- 查询前缀（中文检索指令）：`为这个句子生成表示以用于检索相关文章：`
- 文档侧不加前缀（BGE v1.5 约定）
- 批处理：多段文本拼 batch，减小 ONNX 调用开销；单线程 intra-op（`SetIntraOpNumThreads(2)`）

## 8. 检索与重排

1. 查询编码 → 归一化向量
2. 全量点积（=`os.File` 顺序读 vectors.bin，分块 4MB），取 Top `max(4×K, 40)`
3. 重排分数：

```
score = 0.75 * cosine
      + 0.15 * pathHit        // 查询词与路径 token 的重合比例
      + 0.10 * identifierHit  // 查询中的标识符 / CJK bigram 在块内出现比例
```

4. 同文件默认最多 2 条，超过的降权而非丢弃
5. 回读源码片段（上下文各留 2 行），超过 40 行截断
6. 若 `meta.indexedAt` 超过 24h，或命中文件的 mtime 有更新，附 `stale: true`

## 9. CLI

```
codesseek index [path] [--full] [--workers N] [--quiet]
codesseek search "<query>" [--path DIR] [--top N] [--json] [--prefix PATH]
codesseek status [path]
codesseek projects
codesseek model download [--force]
codesseek serve [--log FILE]
codesseek version
```

- `index` 无参数时索引当前目录；输出：文件总数/新增/更新/删除/跳过、块数、耗时、峰值内存
- `search` 默认人读格式（路径:起止行 + 分数 + 片段），`--json` 供脚本与 Agent 用
- 返回码：0 成功，1 运行错误，2 用法错误，3 未索引

## 10. MCP Server

OpenCode 配置（全局 `~/.config/opencode/opencode.json`）：

```json
{
  "mcp": {
    "servers": {
      "codesseek": {
        "type": "local",
        "command": ["<home>\\bin\\codesseek.exe", "serve"],
        "environment": { "HF_ENDPOINT": "https://hf-mirror.com" },
        "pool": true
      }
    }
  }
}
```

工具定义：

| 工具 | 入参 | 返回 |
|---|---|---|
| `search_code` | `query`(必填), `root?`, `topK?`(默认 8), `pathPrefix?` | `{results: [{path, startLine, endLine, score, symbol, snippet, stale}], index?}` |
| `index_project` | `root?`, `full?` | `{jobId, root, startedAt}`，后台执行立即返回 |
| `index_status` | `root?`, `jobId?` | `{state, processed, total, files, chunks, elapsedMs, error?}` |
| `list_projects` | — | `{projects: [{root, files, chunks, indexedAt, model, dim}]}` |

> MCP 的 `structuredContent` 必须是 JSON 对象（OpenCode 会校验），所以列表结果统一包一层
> `results` / `projects`，不能直接返回数组。

`root` 解析顺序：显式入参 → 从 cwd 向上匹配已索引项目 → cwd 含项目标记（`.git`/`go.mod`/`package.json`…）
→ 唯一已索引项目 → 报错并列出候选。

自动索引（`search_code` 内联完成，Agent 无需先手动 index）：

- 无索引 → 建索引；索引记录的文件有增删改或超过 24h → 增量刷新
- 返回 `index: {action: indexed|refreshed|indexing|refreshing, jobId?, state?, files?, chunks?, elapsedMs?, message?}`
- 同步等待默认 30s（`CODESEEK_AUTO_INDEX_WAIT`），超时转后台任务，Agent 轮询 `index_status` 后重试
- `CODESEEK_AUTO_INDEX=off` 关闭，恢复「先 index_project」合同
- 仅显式 `root` 或 cwd 带项目标记时自动建；磁盘根 / 用户 home 拒绝索引
- 过期检查按 10s/root 缓存，`NeedsRefresh` 用扫描结果与 `files.jsonl` 对比 size+mtime（不哈希）

实现约束：

- 使用 `modelcontextprotocol/go-sdk` v1.8.0（stdio 最高协商 `2025-11-25`）
- **stdout 只允许协议帧**；日志默认写 `<home>/logs/mcp-YYYY-MM-DD.log`，跨本地午夜自动换文件（`internal/dailylog`），`serve --log FILE` 可固定单文件
- 日志目录不可写时回退到 stderr，不影响 MCP 服务；`--log` 的父目录会自动创建
- 工具错误返回 `IsError` + 结构化消息，不 panic
- 模型懒加载：首次 `search_code` 时才初始化 ONNX session，空闲进程不占模型内存
- 同一进程内按 root 缓存 store 句柄，避免每次查询重开文件
- OpenCode `pool: true` 只在 `2026-07-28` 连接间生效；当前 SDK 不支持，故每个工作目录各起一个进程（空闲约 20MB）

## 11. 错误处理

| 场景 | 行为 |
|---|---|
| 模型文件缺失 | 提示 `codesseek model download`，附 `HF_ENDPOINT` 说明 |
| 模型下载失败 | 列出尝试的 URL 与错误，建议换镜像 |
| 索引不存在 | CLI 返回码 3；MCP `search_code` 自动建索引（`CODESEEK_AUTO_INDEX=off` 时返回「project not indexed」） |
| `chunkConfigHash` 不匹配 | 拒绝增量，提示 `index --full` |
| 索引文件损坏（长度/行数不符） | 提示重建，不尝试修复 |
| 并发索引同一项目 | 走 lock，返回「已有任务运行中」 |
| 单文件解析失败 | warning 跳过，最终统计里列数量 |

## 12. 性能与内存预算

| 场景 | 目标 |
|---|---|
| 空闲（MCP 已连接、未查询） | 30–60MB |
| 首次查询后（模型常驻） | 200–350MB |
| 索引峰值（workers=2） | 400–700MB，结束回落 |
| 索引速度 | ≥ 300 文件/分钟（约 6000 文件 ≈ 20 分钟，可后台跑） |
| 查询延迟 | 6000 文件 / 5 万块 ≈ 50–150ms（读盘 100MB + 点积） |
| 磁盘 | 模型 24MB；多个仓库索引合计约 50–150MB |

对比 VectorCode：每窗口空闲 0.4–0.6GB → 单进程 30–60MB，且多项目共享。

参考实测（126 文件 / 1752 块）：

- 索引：`--workers 4` + ONNX 8 线程 **1m24s**，峰值 **292MB**；workers=2 时约 4m50s
- 增量（无改动）：**0.25s**（全部 skipped；模型懒加载）
- MCP：空闲 **21MB**，首次查询加载模型后 **107MB**
- CLI 查询：约 0.6–0.9s（主要是进程启动 + 加载 ONNX；MCP 复用 session 后只有读盘与点积）
- 索引速度仍低于「300 文件/分钟」目标（约 90 文件/分钟），瓶颈是单 ONNX session 串行推理

## 13. 测试与验收

**单元测试**

- chunker：Vue（template/script/style 拆分）、TS 顶层函数、Python 类与方法、Markdown 标题、长块切分与行号正确
- scanner：gitignore 语义（含 `!` 反选）、扩展名白名单、大文件跳过
- store：写入→读取往返、原子替换、损坏检测、增量区间一致性
- searcher：已知向量下的 Top-K 正确性、同文件限流、重排权重

**集成测试**

- 假 embedder（确定性 8 维哈希向量）跑 fixture 仓库：index → search → 修改文件 → 再 index（断言只重算 1 个文件）→ 删除文件（断言孤儿清理）

**模型测试**（`-tags modeltest`）

- 真实模型：中文查询「用户登录失败的处理」应命中含 `login`/`handleLoginError` 的块；英文查询同样可用；相似度顺序单调合理

**端到端验收**

1. `codesseek index <repo>` 成功，输出统计合理
2. 3–5 个真实业务问题中文查询命中预期文件，人工判断前 3 条相关
3. 重复 index 全部 `skipped`（< 2s）
4. `Get-Process` 实测空闲/查询后 RSS 落在预算内
5. `opencode mcp list` 显示 connected、4 个工具可用
6. 同时打开两个项目，`Get-Process codesseek` 不出现漏内存/失控；真正的单进程共享要等 SDK 支持 `2026-07-28` 后开启 `pool`
7. 全流程不访问 huggingface.co，模型/索引只写入配置的 home

## 14. 实施顺序

1. **原型验证**：拉依赖（`GOPROXY=https://goproxy.cn,direct`）、取 `onnxruntime.dll`、下载模型，跑通 tokenizer → ONNX → 512 维向量，并用中文短句验证排序
2. store + chunker + 假 embedder：端到端 index/search/增量
3. 接真模型：索引一个真实仓库，验证中文召回
4. CLI 完善 + MCP Server + OpenCode 注册，测 pool 与内存
5. 索引多个真实仓库，补使用说明

**风险与对策**

| 风险 | 对策 |
|---|---|
| `sugarme/tokenizer` 对 TemplateProcessing 适配不全 | 原型阶段实测；不通换 `daulet/tokenizers`（cgo） |
| ONNX 输入/输出名与预期不符 | 用 `session.GetInputNames()` 打印实际结构 |
| Windows 下 onnxruntime DLL 加载失败 | 二进制同目录放置 DLL；失败时输出明确路径提示 |
| 中文召回差 | 调查询前缀 / 换 `bge-small-zh-v1.5` 非量化版 / 换 `bge-m3`（内存代价大） |
| 大仓库索引慢 | 提高 workers、加 `--prefix` 只索引子目录 |

## 15. 附录：已验证的环境事实

| 项 | 结论 |
|---|---|
| Go | 1.23.5，可自动下载 1.25/1.26 工具链 |
| gcc | 15.2.0（MSYS2），`CGO_ENABLED=1` 可用 |
| Go 代理 | `proxy.golang.org` 超时；`goproxy.cn` / `goproxy.io` / 阿里云 goproxy 可用 |
| HuggingFace | `huggingface.co` 超时；`hf-mirror.com` 可用 |
| onnxruntime.dll | 可从 PyPI `onnxruntime` win_amd64 wheel 提取（约 16MB） |
| tokenizer.json | `Xenova/bge-small-zh-v1.5` 可直接从 hf-mirror 下载，格式已确认：WordPiece / BertNormalizer / BertPreTokenizer / TemplateProcessing / vocab 21128 |
| 磁盘 | 缓存、模型、索引应放在空间充足的盘，不要挤占系统盘 |
| Python | 系统 `python` 是 msys64 且无 pip（不作为依赖） |

**常用命令**

```powershell
$env:GOPROXY = 'https://goproxy.cn,direct'
$env:HF_ENDPOINT = 'https://hf-mirror.com'
go get github.com/modelcontextprotocol/go-sdk@latest
go get github.com/yalue/onnxruntime_go@latest
go get github.com/sugarme/tokenizer@latest
```
# Builds codesseek.
#
#   .\build.ps1                 # self-contained exe (embeds ONNX runtime + model)
#   .\build.ps1 -Slim           # small exe that uses files on disk
#   .\build.ps1 -RuntimeDll D:\path\onnxruntime.dll -ModelDir D:\path\bge-small-zh-v1.5
#
# A full build stages the binaries into internal\assets\data (git-ignored) and
# compiles with `-tags embed`. Without that tag nothing is embedded, so a plain
# `go build` or `go test` always produces the slim variant and the staged files
# can stay on disk between builds.
#
# Asset lookup order: explicit arguments, environment, local/installed layout,
# then the staging directory left by an earlier build. That makes a rebuild
# work without arguments once the assets have been staged once.

[CmdletBinding()]
param(
    [string]$RuntimeDll,
    [string]$ModelDir,
    [string]$Output = 'bin\codesseek.exe',
    [switch]$Slim
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSCommandPath
Set-Location $root

$dataDir = Join-Path $root 'internal\assets\data'
$runtimeStage = Join-Path $dataDir 'runtime'
$modelStageRoot = Join-Path $dataDir 'model'
$modelStage = Join-Path $modelStageRoot 'bge-small-zh-v1.5'

$requiredModelFiles = @('model_quantized.onnx', 'tokenizer.json', 'config.json')
$optionalModelFiles = @('tokenizer_config.json', 'vocab.txt')

function Same-Path([string]$PathA, [string]$PathB) {
    if (-not $PathA -or -not $PathB) { return $false }
    return ([IO.Path]::GetFullPath($PathA) -ieq [IO.Path]::GetFullPath($PathB))
}

function Resolve-RuntimeDll([string]$Path) {
    if ($Path -and (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return (Resolve-Path -LiteralPath $Path).Path
    }
    return $null
}

function Resolve-ModelDir([string]$Path) {
    if (-not $Path) { return $null }
    if (Test-Path -LiteralPath (Join-Path $Path 'model_quantized.onnx') -PathType Leaf) {
        return (Resolve-Path -LiteralPath $Path).Path
    }
    $nested = Join-Path $Path 'bge-small-zh-v1.5'
    if (Test-Path -LiteralPath (Join-Path $nested 'model_quantized.onnx') -PathType Leaf) {
        return (Resolve-Path -LiteralPath $nested).Path
    }
    return $null
}

function Get-TargetPath([string]$Path) {
    if ([IO.Path]::IsPathRooted($Path)) { return [IO.Path]::GetFullPath($Path) }
    return [IO.Path]::GetFullPath((Join-Path $root $Path))
}

# Builds to a temp file first. That lets a rebuild succeed while an older
# codesseek.exe is still running (an MCP server): Windows refuses to overwrite a
# mapped image, but it allows renaming it out of the way.
function Invoke-GoBuild([string[]]$GoArgs, [string]$Label) {
    $target = Get-TargetPath $Output
    $parent = Split-Path -Parent $target
    if ($parent) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }

    $tmp = "$target.new"
    Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
    & go build @GoArgs -o $tmp .\cmd\codesseek
    if ($LASTEXITCODE -ne 0) {
        Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
        exit $LASTEXITCODE
    }

    if (Test-Path -LiteralPath $target) {
        try {
            Move-Item -LiteralPath $tmp -Destination $target -Force
        } catch {
            $backup = "$target.old-" + (Get-Date -Format 'yyyyMMddHHmmss')
            Move-Item -LiteralPath $target -Destination $backup -Force
            Move-Item -LiteralPath $tmp -Destination $target -Force
            Write-Warning "replaced a running executable; the old one is at $backup"
        }
    } else {
        Move-Item -LiteralPath $tmp -Destination $target
    }

    $size = [math]::Round((Get-Item -LiteralPath $target).Length / 1MB, 1)
    Write-Host "built $target ($size MB, $Label)"
}

if ($Slim) {
    Invoke-GoBuild -GoArgs @() -Label 'slim'
    exit 0
}

# --- ONNX runtime -----------------------------------------------------------

$reuseRuntimeStage = $false
if ($RuntimeDll) {
    $givenRuntime = $RuntimeDll
    $RuntimeDll = Resolve-RuntimeDll $RuntimeDll
    if (-not $RuntimeDll) { throw "onnxruntime.dll not found at $givenRuntime" }
} else {
    $candidates = @()
    if ($env:ONNXRUNTIME_DLL) { $candidates += $env:ONNXRUNTIME_DLL }
    $candidates += (Join-Path $root 'bin\onnxruntime.dll')
    if ($env:CODESEEK_HOME) { $candidates += (Join-Path $env:CODESEEK_HOME 'bin\onnxruntime.dll') }
    $candidates += (Join-Path $HOME '.codesseek\bin\onnxruntime.dll')
    foreach ($c in $candidates) {
        $RuntimeDll = Resolve-RuntimeDll $c
        if ($RuntimeDll) { break }
    }
    if (-not $RuntimeDll -and (Test-Path -LiteralPath (Join-Path $runtimeStage 'onnxruntime.dll') -PathType Leaf)) {
        $RuntimeDll = (Resolve-Path -LiteralPath $runtimeStage).Path
        $reuseRuntimeStage = $true
    }
    if (-not $RuntimeDll) {
        throw "onnxruntime.dll not found. Pass -RuntimeDll <path>, set ONNXRUNTIME_DLL, or stage it once with a full build."
    }
}
if (Same-Path $RuntimeDll (Join-Path $runtimeStage 'onnxruntime.dll')) {
    $reuseRuntimeStage = $true
}

if ($reuseRuntimeStage) {
    Write-Host "reusing staged ONNX runtime at $runtimeStage"
    if (-not (Test-Path -LiteralPath (Join-Path $runtimeStage 'onnxruntime_providers_shared.dll') -PathType Leaf)) {
        Write-Warning "onnxruntime_providers_shared.dll is missing next to the staged onnxruntime.dll"
    }
} else {
    Write-Host "staging ONNX runtime from $RuntimeDll"
    Remove-Item -LiteralPath $runtimeStage -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path $runtimeStage | Out-Null
    Copy-Item -LiteralPath $RuntimeDll -Destination (Join-Path $runtimeStage 'onnxruntime.dll')
    $providers = Join-Path (Split-Path -Parent $RuntimeDll) 'onnxruntime_providers_shared.dll'
    if (Test-Path -LiteralPath $providers -PathType Leaf) {
        Copy-Item -LiteralPath $providers -Destination $runtimeStage
    } else {
        Write-Warning "onnxruntime_providers_shared.dll not found next to $RuntimeDll"
    }
}

# --- model ------------------------------------------------------------------

$reuseModelStage = $false
if ($ModelDir) {
    $givenModel = $ModelDir
    $ModelDir = Resolve-ModelDir $ModelDir
    if (-not $ModelDir) { throw "model not found under $givenModel (expected model_quantized.onnx)" }
} else {
    $candidates = @()
    if ($env:CODESEEK_MODEL_DIR) { $candidates += $env:CODESEEK_MODEL_DIR }
    $candidates += (Join-Path $root 'models')
    if ($env:CODESEEK_HOME) { $candidates += (Join-Path $env:CODESEEK_HOME 'models') }
    $candidates += (Join-Path $HOME '.codesseek\models')
    # Installed layout: <home>\bin\onnxruntime.dll -> <home>\models
    $candidates += (Join-Path (Split-Path -Parent (Split-Path -Parent $RuntimeDll)) 'models')

    foreach ($c in $candidates) {
        $ModelDir = Resolve-ModelDir $c
        if ($ModelDir) { break }
    }
    $stagedModelComplete = $true
    foreach ($name in $requiredModelFiles) {
        if (-not (Test-Path -LiteralPath (Join-Path $modelStage $name) -PathType Leaf)) {
            $stagedModelComplete = $false
            break
        }
    }
    if (-not $ModelDir -and $stagedModelComplete) {
        $ModelDir = (Resolve-Path -LiteralPath $modelStage).Path
        $reuseModelStage = $true
    }
    if (-not $ModelDir) {
        throw "model not found. Pass -ModelDir <dir>, set CODESEEK_MODEL_DIR, or stage it once with a full build."
    }
}
if (Same-Path $ModelDir $modelStage) { $reuseModelStage = $true }

if ($reuseModelStage) {
    Write-Host "reusing staged model at $modelStage"
} else {
    Write-Host "staging model from $ModelDir"
    Remove-Item -LiteralPath $modelStageRoot -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path $modelStage | Out-Null
    foreach ($name in ($requiredModelFiles + $optionalModelFiles)) {
        $src = Join-Path $ModelDir $name
        if (Test-Path -LiteralPath $src -PathType Leaf) {
            Copy-Item -LiteralPath $src -Destination $modelStage
        } elseif ($requiredModelFiles -contains $name) {
            throw "missing required model file $name in $ModelDir"
        }
    }
}

# --- build ------------------------------------------------------------------

Invoke-GoBuild -GoArgs @('-tags', 'embed') -Label 'embedded assets'
$assetsSize = [math]::Round(((Get-ChildItem -LiteralPath $dataDir -Recurse -File | Measure-Object -Property Length -Sum).Sum) / 1MB, 1)
Write-Host "embedded $assetsSize MB of staged assets; run .\build.ps1 -Slim for the small on-disk variant"
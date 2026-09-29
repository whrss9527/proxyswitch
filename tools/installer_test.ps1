# 安装包的端到端测试（Windows）：静默安装、覆盖安装（升级）、检查装好的东西，再用 ProxySwitch.exe --uninstall --quiet
# 卸载，检查程序、开始菜单快捷方式、卸载项、开机自启和链接的登记都清理掉了，配置留着。
# 会改当前用户的注册表和开始菜单，在 CI 或者测试用的电脑上运行。
#
# 用法：pwsh tools/installer_test.ps1 -Installer dist/ProxySwitch-Setup.exe [-Version 2.4.0]
param(
    [Parameter(Mandatory = $true)][string]$Installer,
    # 安装包的版本号，默认取 Makefile 里的 VERSION。
    [string]$Version = ""
)
$ErrorActionPreference = "Stop"
$failures = 0

function Check([bool]$condition, [string]$message) {
    if ($condition) {
        Write-Host "ok   $message"
    } else {
        Write-Host "FAIL $message"
        $script:failures++
    }
}

if (-not $Version) {
    $Version = (Select-String -Path (Join-Path $PSScriptRoot "..\Makefile") -Pattern '^VERSION \?= (.+)$').Matches[0].Groups[1].Value.Trim()
}
$Installer = (Resolve-Path $Installer).Path
$installDir = Join-Path $env:LOCALAPPDATA "Programs\ProxySwitch"
$exe = Join-Path $installDir "ProxySwitch.exe"
$uninstallKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\ProxySwitch"
$shortcut = Join-Path ([Environment]::GetFolderPath("Programs")) "ProxySwitch.lnk"
$runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
$linkKey = "HKCU:\Software\Classes\proxyswitch"
$dataDir = Join-Path $env:APPDATA "ProxySwitch"
$config = Join-Path $dataDir "config.jsonc"

function Install {
    return (Start-Process -FilePath $Installer -ArgumentList "/S" -Wait -PassThru).ExitCode
}

# ---------- 安装 ----------
Check ((Install) -eq 0) "静默安装成功"
Check (Test-Path $exe) "程序装到 %LOCALAPPDATA%\Programs\ProxySwitch"
Check (Test-Path $shortcut) "开始菜单里有 ProxySwitch"
$entry = Get-ItemProperty $uninstallKey
Check ($entry.DisplayName -eq "ProxySwitch" -and $entry.InstallLocation -eq $installDir) "「设置 → 应用」里有卸载项，记着安装目录"
Check ($entry.DisplayVersion -eq $Version) "卸载项显示的版本是 $Version"
Check ($entry.UninstallString -eq "`"$exe`" --uninstall" -and $entry.QuietUninstallString -eq "`"$exe`" --uninstall --quiet") "卸载项执行 ProxySwitch.exe --uninstall"
Check ($entry.StartMenuShortcut -eq $shortcut) "卸载项记着开始菜单快捷方式的位置"
Check ($entry.EstimatedSize -gt 1000) "卸载项有占用空间"
Check ((Get-Item $exe).VersionInfo.ProductName -eq "ProxySwitch") "装的是 ProxySwitch 的程序"

# ---------- 覆盖安装（升级） ----------
Check ((Install) -eq 0) "覆盖安装成功"
Check ((Test-Path $exe) -and -not (Test-Path "$exe.old")) "覆盖安装后程序还在，没有留下旧程序文件"
Check ((Get-ChildItem $installDir).Count -eq 1) "安装目录里只有 ProxySwitch.exe"

# ---------- 卸载 ----------
# 模拟程序运行时做的登记：开机自启、proxyswitch:// 链接；再放一份配置，静默卸载应该留着它。
New-ItemProperty -Path $runKey -Name "ProxySwitch" -Value "`"$exe`" --autostart" -PropertyType String -Force | Out-Null
New-Item -Path "$linkKey\shell\open\command" -Force | Out-Null
Set-ItemProperty -Path $linkKey -Name "(default)" -Value "URL:ProxySwitch"
New-ItemProperty -Path $linkKey -Name "URL Protocol" -Value "" -PropertyType String -Force | Out-Null
Set-ItemProperty -Path "$linkKey\shell\open\command" -Name "(default)" -Value "`"$exe`" `"%1`""
New-Item -ItemType Directory -Path $dataDir -Force | Out-Null
$hadConfig = Test-Path $config
if (-not $hadConfig) {
    Set-Content -Path $config -Value "{}" -Encoding utf8
}

$uninstall = Start-Process -FilePath $exe -ArgumentList "--uninstall", "--quiet" -Wait -PassThru
Check ($uninstall.ExitCode -eq 0) "静默卸载成功"
# 程序文件在卸载进程退出后由后台的批处理删掉。
$deadline = (Get-Date).AddSeconds(30)
while ((Test-Path $installDir) -and (Get-Date) -lt $deadline) {
    Start-Sleep -Milliseconds 500
}
Check (-not (Test-Path $installDir)) "卸载后删掉了程序和安装目录"
Check (-not (Test-Path $uninstallKey)) "卸载后删掉了卸载项"
Check (-not (Test-Path $shortcut)) "卸载后删掉了开始菜单的快捷方式"
Check ($null -eq (Get-ItemProperty $runKey -Name "ProxySwitch" -ErrorAction SilentlyContinue)) "卸载后取消了开机自启"
Check (-not (Test-Path $linkKey)) "卸载后取消了 proxyswitch:// 链接的登记"
Check (Test-Path $config) "静默卸载留着配置"
Start-Sleep -Seconds 2
Check (@(Get-ChildItem $env:TEMP -Filter "proxyswitch-uninstall-*.cmd" -ErrorAction SilentlyContinue).Count -eq 0) "删除用的批处理删掉了自己"
if (-not $hadConfig) {
    Remove-Item -Recurse -Force $dataDir -ErrorAction SilentlyContinue
}

# ---------- 装好后再装一次，确认卸载后可以重新安装 ----------
Check ((Install) -eq 0) "卸载后重新安装成功"
Check ((Test-Path $exe) -and (Test-Path $uninstallKey)) "重新安装后程序和卸载项都在"
$final = Start-Process -FilePath $exe -ArgumentList "--uninstall", "--quiet" -Wait -PassThru
Check ($final.ExitCode -eq 0) "再次卸载成功"

if ($failures -gt 0) {
    Write-Host "$failures 项失败"
    exit 1
}
Write-Host "全部通过"

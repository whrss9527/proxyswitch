; ProxySwitch 安装程序：装到当前用户的 %LOCALAPPDATA%\Programs\ProxySwitch，不需要管理员权限。
; 一个安装包里带 x64 和 ARM64 两个程序，安装时按处理器选。开始菜单里加快捷方式，在「设置 → 应用」里登记
; 卸载项；卸载由 ProxySwitch.exe --uninstall 完成（见 install_windows.go）。
;
; 编译（先编译好 dist 里的两个 exe）：make installer，或者
;   makensis -DVERSION=2.4.0 -DDIST=/path/to/dist installer/ProxySwitch.nsi

Unicode true
ManifestDPIAware true
ManifestSupportedOS all
RequestExecutionLevel user
SetCompressor /SOLID lzma

!ifndef VERSION
  !error "请用 -DVERSION=x.y.z 指定版本号"
!endif
!ifndef DIST
  !define DIST "../dist"
!endif

!define APP "ProxySwitch"
!define EXE "ProxySwitch.exe"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP}"
; 和程序里的一致：托盘窗口的类名（tray_windows.go）和单实例互斥量（main_windows.go）。
!define TRAY_WINDOW_CLASS "ProxySwitchTrayWindow"
!define INSTANCE_MUTEX "Local\ProxySwitch-3f1c2a9e"
!define REPOSITORY "https://github.com/whrss9527/proxyswitch"

Name "${APP}"
OutFile "${DIST}/ProxySwitch-Setup.exe"
InstallDir "$LOCALAPPDATA\Programs\${APP}"
InstallDirRegKey HKCU "${UNINSTALL_KEY}" "InstallLocation"
BrandingText "${APP} ${VERSION}"
ShowInstDetails nevershow

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"
!include "WinVer.nsh"
!include "FileFunc.nsh"
!include "WinMessages.nsh"

!define MUI_ICON "../assets/app.ico"
!define MUI_WELCOMEFINISHPAGE_BITMAP "../assets/installer-side.bmp"
!define MUI_ABORTWARNING

!define MUI_WELCOMEPAGE_TITLE "安装 ${APP} ${VERSION}"
!define MUI_WELCOMEPAGE_TEXT "ProxySwitch 是任务栏托盘里的代理开关。$\r$\n$\r$\n它会装在你自己的用户目录里，不需要管理员权限；开始菜单里会有 ProxySwitch，以后可以在「设置 → 应用」里卸载。$\r$\n$\r$\n如果 ProxySwitch 正在运行，安装时会先让它退出。配置和订阅都会保留。"
!define MUI_PAGE_CUSTOMFUNCTION_SHOW WelcomeShow
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_TITLE "${APP} 装好了"
!define MUI_FINISHPAGE_TEXT "运行后任务栏右下角会出现 ProxySwitch 的图标。以后可以在开始菜单里找到它。"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "运行 ProxySwitch"
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_LANGUAGE "SimpChinese"

VIProductVersion "${VERSION}.0"
VIAddVersionKey /LANG=${LANG_SIMPCHINESE} "ProductName" "${APP}"
VIAddVersionKey /LANG=${LANG_SIMPCHINESE} "FileDescription" "${APP} 安装程序"
VIAddVersionKey /LANG=${LANG_SIMPCHINESE} "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_SIMPCHINESE} "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_SIMPCHINESE} "LegalCopyright" "MIT License"
VIAddVersionKey /LANG=${LANG_SIMPCHINESE} "Comments" "${REPOSITORY}"

; WasRunning 表示安装前 ProxySwitch 在运行：静默安装完后重新打开它。
Var WasRunning

Function .onInit
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_ICONSTOP "ProxySwitch 需要 Windows 10 或 Windows 11。" /SD IDOK
    Abort
  ${EndIf}
  ${IfNot} ${IsNativeAMD64}
  ${AndIfNot} ${IsNativeARM64}
    MessageBox MB_ICONSTOP "ProxySwitch 需要 64 位的 Windows。" /SD IDOK
    Abort
  ${EndIf}
FunctionEnd

; 欢迎页的「下一步」直接开始安装，按钮上写「安装」。
Function WelcomeShow
  GetDlgItem $0 $HWNDPARENT 1
  SendMessage $0 ${WM_SETTEXT} 0 "STR:安装(&I)"
FunctionEnd

; 让正在运行的 ProxySwitch 退出（和从托盘菜单退出一样，会关闭它开启的代理和内核），等它的进程结束：
; 进程结束前一直持有单实例互斥量。
Function CloseRunning
  StrCpy $WasRunning 0
  retry:
  FindWindow $0 "${TRAY_WINDOW_CLASS}"
  ${If} $0 <> 0
    StrCpy $WasRunning 1
    DetailPrint "正在退出 ProxySwitch…"
    SendMessage $0 ${WM_CLOSE} 0 0 /TIMEOUT=5000
  ${EndIf}
  StrCpy $1 0
  ${Do}
    System::Call 'kernel32::OpenMutexW(i 0x00100000, i 0, w "${INSTANCE_MUTEX}") p .r2'
    ${If} $2 == 0
      ${Break}
    ${EndIf}
    System::Call 'kernel32::CloseHandle(p r2)'
    StrCpy $WasRunning 1
    ${If} $1 >= 40
      MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "ProxySwitch 还在运行。请从托盘图标的菜单里选「退出」，然后点「重试」。" /SD IDCANCEL IDRETRY retry
      Abort
    ${EndIf}
    Sleep 500
    IntOp $1 $1 + 1
  ${Loop}
FunctionEnd

Section
  SetShellVarContext current
  Call CloseRunning
  SetOutPath "$INSTDIR"
  ; 旧版本刚退出时程序文件可能还没释放：先改名让开（运行中的程序也能改名），新版本启动时会删掉它。
  Delete "$INSTDIR\${EXE}.old"
  ${If} ${FileExists} "$INSTDIR\${EXE}"
    Rename "$INSTDIR\${EXE}" "$INSTDIR\${EXE}.old"
  ${EndIf}
  ${If} ${IsNativeARM64}
    File "/oname=${EXE}" "${DIST}/ProxySwitch-arm64.exe"
  ${Else}
    File "${DIST}/ProxySwitch.exe"
  ${EndIf}
  Delete "$INSTDIR\${EXE}.old"

  CreateShortcut "$SMPROGRAMS\${APP}.lnk" "$INSTDIR\${EXE}" "" "$INSTDIR\${EXE}" 0 SW_SHOWNORMAL "" "任务栏托盘里的代理开关"

  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${APP}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\${EXE},0"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "whrss9527"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\${EXE}" --uninstall'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "QuietUninstallString" '"$INSTDIR\${EXE}" --uninstall --quiet'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "URLInfoAbout" "${REPOSITORY}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "HelpLink" "${REPOSITORY}/issues"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "StartMenuShortcut" "$SMPROGRAMS\${APP}.lnk"
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "EstimatedSize" $0
SectionEnd

; 静默安装（/S）时没有完成页：安装前在运行的，装好后重新打开。
Function .onInstSuccess
  ${If} ${Silent}
  ${AndIf} $WasRunning == 1
    Exec '"$INSTDIR\${EXE}"'
  ${EndIf}
FunctionEnd

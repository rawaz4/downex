; Downex Setup: per-user Windows installer (DESKTOP-SPEC section 6).
;
; Built by scripts\build-windows.ps1 -Installer, which passes:
;   /DVERSION=<version>        shown to the user and written to the Uninstall entry
;   /DVIVERSION=<a.b.c.d>      numeric version for the file's version resource
;   /DARCH=amd64|arm64         architecture of the programs in SRCDIR
;   /DSRCDIR=<dir>             folder with downexd.exe, downex.exe, downex-nmhost.exe,
;                              downex-ui.exe and extension\chrome-mv3, extension\firefox-mv3
;   /DOUTFILE=<file>           the Setup program to write
; Compiling runs installer\extension-files.ps1 (Windows PowerShell) on SRCDIR\extension.
;
; Installs into %LOCALAPPDATA%\Programs\Downex without administrator rights. Silent use:
;   Downex-Setup-<version>.exe /S [/RELAUNCH] [/D=<folder>]   (/D must be last and unquoted)
;   <folder>\uninstall.exe /S                           (keeps the user's data)
; /RELAUNCH (the in-app update, internal/update) opens the window again after a silent
; install when Setup closed it. A silent install keeps the desktop shortcut as the user left it.
; The in-app update also passes /D=<the running installation's folder>, so that Setup
; replaces that folder; an installation the Uninstall entry names elsewhere is removed.
;
; Replacing a program: it is renamed aside to <name>.old (Windows allows that also while
; the program runs, as a browser may run downex-nmhost.exe again at any moment), and the
; aside copies are deleted once the new files are in place; one still in use is deleted by
; the next Setup or the uninstaller. When Setup fails, .onInstFailed puts the aside copies
; back and starts the daemon again, so the earlier version keeps working.

Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma
RequestExecutionLevel user

!ifndef VERSION
  !define VERSION "dev"
!endif
!ifndef VIVERSION
  !define VIVERSION "0.0.0.0"
!endif
!ifndef ARCH
  !define ARCH "amd64"
!endif
!ifndef SRCDIR
  !define SRCDIR "${__FILEDIR__}\..\bin"
!endif
!ifndef OUTFILE
  !define OUTFILE "${SRCDIR}\Downex-Setup-${VERSION}.exe"
!endif

!define PRODUCT "Downex"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Downex"
!define RUNKEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define RUNVALUE "Downex"
; CreateProcess flags for a daemon without a console: DETACHED_PROCESS |
; CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW, first also CREATE_BREAKAWAY_FROM_JOB
; (refused inside a job that forbids it, so it is retried without).
!define DETACH_FLAGS 0x08000208
!define DETACH_FLAGS_BREAKAWAY 0x09000208

!include MUI2.nsh
!include LogicLib.nsh
!include Sections.nsh
!include FileFunc.nsh
!include StrFunc.nsh
!include x64.nsh
!include WinVer.nsh

${StrStr}

Name "${PRODUCT}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\Downex"
InstallDirRegKey HKCU "${UNINSTKEY}" "InstallLocation"
BrandingText "${PRODUCT} ${VERSION}"
ShowInstDetails show
ShowUninstDetails show
AllowSkipFiles off

VIProductVersion "${VIVERSION}"
VIFileVersion "${VIVERSION}"
VIAddVersionKey /LANG=1033 "ProductName" "${PRODUCT}"
VIAddVersionKey /LANG=1033 "CompanyName" "${PRODUCT}"
VIAddVersionKey /LANG=1033 "FileDescription" "${PRODUCT} Setup"
VIAddVersionKey /LANG=1033 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=1033 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=1033 "LegalCopyright" "Copyright (C) 2026 The Downex authors. GPL-3.0-or-later"

Var PrevDir        ; InstallLocation of an existing installation, or ""
Var Registered     ; uninstaller: 1 when $INSTDIR is the installation the Uninstall key names
Var PowerShellExe  ; 64-bit Windows PowerShell where there is one (it can see 64-bit processes)
Var Stopped        ; 1 once Setup has stopped the Downex programs of $INSTDIR
Var Upgrade        ; 1 when $INSTDIR held a Downex (its downexd.exe) before Setup stopped it
Var AsideNmhost    ; the aside copies of the programs being replaced ("" = none)
Var AsideCli
Var AsideDaemon
Var AsideUI

; ---------------------------------------------------------------------------------------
; Pages

!define MUI_ICON "${__FILEDIR__}\..\packaging\windows\icon.ico"
!define MUI_UNICON "${__FILEDIR__}\..\packaging\windows\icon.ico"
!define MUI_ABORTWARNING
!define MUI_COMPONENTSPAGE_SMALLDESC

!define MUI_WELCOMEPAGE_TEXT "Setup will install ${PRODUCT} ${VERSION} for your account only; it does not need administrator rights.$\r$\n$\r$\nIf ${PRODUCT} is running, Setup closes its window and stops its daemon before it replaces the files. Running downloads are paused and continue afterwards.$\r$\n$\r$\nClick Next to continue."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${__FILEDIR__}\LICENSE.txt"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES

!define MUI_FINISHPAGE_TEXT "${PRODUCT} has been installed.$\r$\n$\r$\nTo hand downloads from Firefox, Edge, Chrome or Brave to ${PRODUCT}, add its browser extension once from the Chrome Web Store or Firefox Add-ons (Settings > Integration in ${PRODUCT} has both buttons). The link below shows how for each browser."
!define MUI_FINISHPAGE_RUN "$INSTDIR\downex-ui.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${PRODUCT}"
!define MUI_FINISHPAGE_LINK "How to add the ${PRODUCT} browser extension"
!define MUI_FINISHPAGE_LINK_LOCATION "$INSTDIR\EXTENSION.txt"
!define MUI_FINISHPAGE_NOREBOOTSUPPORT
; The text takes 7 lines: without this, a page with the Start checkbox gives it 5 and cuts
; it off mid-sentence (Modern UI 2's Finish.nsh).
!define MUI_FINISHPAGE_TEXT_LARGE
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; ---------------------------------------------------------------------------------------
; Functions shared by Setup and the uninstaller ("un." copies).

!macro SHARED_FUNCTIONS un

; Sets $PowerShellExe. Setup is a 32-bit program; a 32-bit PowerShell cannot read the
; executable paths of 64-bit processes, so the native one is used through Sysnative.
Function ${un}FindPowerShell
  StrCpy $PowerShellExe "$WINDIR\Sysnative\WindowsPowerShell\v1.0\powershell.exe"
  ${IfNot} ${FileExists} "$PowerShellExe"
    StrCpy $PowerShellExe "$SYSDIR\WindowsPowerShell\v1.0\powershell.exe"
  ${EndIf}
FunctionEnd

; Stops the Downex programs that run from folder $R0 (stop-downex.ps1): the window, the
; daemon (through $R0\downex.exe daemon stop), then whatever is left from that folder.
; Programs of a Downex in another folder are not touched.
Function ${un}StopDownexIn
  Push $0
  InitPluginsDir
  File "/oname=$PLUGINSDIR\stop-downex.ps1" "${__FILEDIR__}\stop-downex.ps1"
  Call ${un}FindPowerShell
  System::Call 'kernel32::SetEnvironmentVariableW(w "DOWNEX_SETUP_DIR", w R0) i'
  System::Call 'kernel32::SetEnvironmentVariableW(w "DOWNEX_SETUP_CLI", w "$R0\downex.exe") i'
  System::Call 'kernel32::SetEnvironmentVariableW(w "DOWNEX_SETUP_SCRIPT", w "$PLUGINSDIR\stop-downex.ps1") i'
  System::Call 'kernel32::SetEnvironmentVariableW(w "DOWNEX_SETUP_WINDOW_MARK", w "$PLUGINSDIR\window-closed") i'
  DetailPrint "Stopping ${PRODUCT} programs in $R0"
  nsExec::ExecToLog '"$PowerShellExe" -NoLogo -NoProfile -NonInteractive -Command "& ([ScriptBlock]::Create([IO.File]::ReadAllText($$env:DOWNEX_SETUP_SCRIPT)))"'
  Pop $0
  ${If} $0 != 0
    DetailPrint "Stopping ${PRODUCT} programs did not finish ($0)."
  ${EndIf}
  System::Call 'kernel32::SetEnvironmentVariableW(w "DOWNEX_SETUP_DIR", p 0) i'
  System::Call 'kernel32::SetEnvironmentVariableW(w "DOWNEX_SETUP_CLI", p 0) i'
  System::Call 'kernel32::SetEnvironmentVariableW(w "DOWNEX_SETUP_SCRIPT", p 0) i'
  System::Call 'kernel32::SetEnvironmentVariableW(w "DOWNEX_SETUP_WINDOW_MARK", p 0) i'
  Pop $0
FunctionEnd

!macroend

!insertmacro SHARED_FUNCTIONS ""
!insertmacro SHARED_FUNCTIONS "un."

; ---------------------------------------------------------------------------------------
; Replacing the programs (Setup)

; Deletes the aside copies SetAside made in folder DIR, and only those: <program>.old and
; <program>.<n>.old of the four programs. Never "*.old": the folder may be shared with
; other files (an install into an existing folder).
!macro DELETE_ASIDE_COPY DIR NAME
  Delete "${DIR}\${NAME}.old"
  Delete "${DIR}\${NAME}.?.old"
!macroend
!macro DELETE_ASIDE_COPIES DIR
  !insertmacro DELETE_ASIDE_COPY "${DIR}" "downex-nmhost.exe"
  !insertmacro DELETE_ASIDE_COPY "${DIR}" "downex.exe"
  !insertmacro DELETE_ASIDE_COPY "${DIR}" "downexd.exe"
  !insertmacro DELETE_ASIDE_COPY "${DIR}" "downex-ui.exe"
!macroend

; Renames program $R2 aside to $R2.old (or $R2.1.old to $R2.9.old while an earlier aside
; copy is still in use) and returns that name in $R3, or "" when there is no $R2. Renaming
; works while the program runs; when it fails all the same (another program has the file
; open), retries for 10 seconds, stops the Downex programs in $R0 again halfway, then asks
; to retry or cancel. Silent: cancels, and .onInstFailed restores what was set aside.
Function SetAside
  Push $R4
  Push $R5
  Push $R6
  StrCpy $R3 ""
  StrCpy $R5 0
  ${DoWhile} ${FileExists} "$R2"
    StrCpy $R4 "$R2.old"
    StrCpy $R6 0
    ${DoWhile} ${FileExists} "$R4"
      Delete "$R4"
      ${IfNot} ${FileExists} "$R4"
        ${Break}
      ${EndIf}
      IntOp $R6 $R6 + 1
      ${If} $R6 > 9
        ${Break}
      ${EndIf}
      StrCpy $R4 "$R2.$R6.old"
    ${Loop}
    ClearErrors
    Rename "$R2" "$R4"
    ${IfNot} ${Errors}
      StrCpy $R3 "$R4"
      ${Break}
    ${EndIf}
    IntOp $R5 $R5 + 1
    ${If} $R5 = 10
      Call StopDownexIn
    ${ElseIf} $R5 >= 20
      MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "$R2 is in use.$\r$\n$\r$\nClose ${PRODUCT} and any browser window whose ${PRODUCT} extension is busy, then click Retry." /SD IDCANCEL IDRETRY retry
      DetailPrint "$R2 is in use; cancelled."
      Abort "$R2 is in use."
      retry:
      StrCpy $R5 0
      Call StopDownexIn
    ${EndIf}
    Sleep 500
  ${Loop}
  Pop $R6
  Pop $R5
  Pop $R4
FunctionEnd

; Sets the programs of $R0 aside (SetAside). The relay goes first and the window last, so
; that a Setup that gives up on one of them leaves the window in place as long as it can.
Function SetProgramsAside
  Push $R2
  Push $R3
  StrCpy $R2 "$R0\downex-nmhost.exe"
  Call SetAside
  StrCpy $AsideNmhost $R3
  StrCpy $R2 "$R0\downex.exe"
  Call SetAside
  StrCpy $AsideCli $R3
  StrCpy $R2 "$R0\downexd.exe"
  Call SetAside
  StrCpy $AsideDaemon $R3
  StrCpy $R2 "$R0\downex-ui.exe"
  Call SetAside
  StrCpy $AsideUI $R3
  Pop $R3
  Pop $R2
FunctionEnd

; Deletes the aside copies once the new programs are in place; one that is still running
; stays until the next Setup or the uninstaller deletes it.
Function DeleteAside
  ${If} $AsideNmhost != ""
    Delete "$AsideNmhost"
  ${EndIf}
  ${If} $AsideCli != ""
    Delete "$AsideCli"
  ${EndIf}
  ${If} $AsideDaemon != ""
    Delete "$AsideDaemon"
  ${EndIf}
  ${If} $AsideUI != ""
    Delete "$AsideUI"
  ${EndIf}
  StrCpy $AsideNmhost ""
  StrCpy $AsideCli ""
  StrCpy $AsideDaemon ""
  StrCpy $AsideUI ""
FunctionEnd

; Puts aside copy $R3 back as program $R2, replacing a new file Setup may have written.
Function RestoreAside
  ${If} $R3 == ""
  ${OrIfNot} ${FileExists} "$R3"
    Return
  ${EndIf}
  Delete "$R2"
  ClearErrors
  Rename "$R3" "$R2"
  ${If} ${Errors}
    DetailPrint "Could not restore $R2 from $R3."
  ${Else}
    DetailPrint "Restored $R2."
  ${EndIf}
FunctionEnd

; ---------------------------------------------------------------------------------------
; Setup

Function .onInit
  !if "${ARCH}" == "arm64"
    ${IfNot} ${IsNativeARM64}
      MessageBox MB_OK|MB_ICONSTOP "This ${PRODUCT} Setup is for Windows on ARM64 computers." /SD IDOK
      Abort
    ${EndIf}
  !else
    ${IfNot} ${RunningX64}
      MessageBox MB_OK|MB_ICONSTOP "${PRODUCT} needs 64-bit Windows." /SD IDOK
      Abort
    ${EndIf}
  !endif
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_OK|MB_ICONSTOP "${PRODUCT} needs Windows 10 or later." /SD IDOK
    Abort
  ${EndIf}
  ; The window is an x64 program also in the ARM64 package (its Direct2D calls are x64
  ; only), and Windows on ARM runs x64 programs from Windows 11 on: Windows 10 on ARM
  ; emulates 32-bit x86 only.
  ${If} ${IsNativeARM64}
  ${AndIfNot} ${AtLeastWin11}
    MessageBox MB_OK|MB_ICONSTOP "${PRODUCT} needs Windows 11 on ARM64 computers: Windows 10 on ARM cannot run its x64 window." /SD IDOK
    Abort
  ${EndIf}
  UserInfo::GetAccountType
  Pop $0
  ${If} $0 == "Admin"
    MessageBox MB_OKCANCEL|MB_ICONEXCLAMATION "Setup is running with administrator rights.$\r$\n$\r$\n${PRODUCT} is installed for the account that runs Setup, and its daemon does not run with administrator rights. Unless that is what you want, cancel and start Setup normally (not with 'Run as administrator').$\r$\n$\r$\nContinue anyway?" /SD IDOK IDOK +2
    Abort
  ${EndIf}
  ReadRegStr $PrevDir HKCU "${UNINSTKEY}" "InstallLocation"
  Call KeepShortcutChoice
FunctionEnd

; An installation in another folder than $INSTDIR is removed with its own uninstaller
; (silently, keeping the user's data), so that one installation remains. The same folder
; spelt another way (a short 8.3 name, a junction or subst drive; the in-app update passes
; the running installation's folder with /D=) is recognised by a file Setup writes into
; $INSTDIR and finds in $PrevDir: its uninstaller must never run under the new files. A
; marker an earlier Setup left there (it was ended between writing and deleting it) is
; deleted first, so that it cannot pass another folder off as this one; when no marker can
; be written, the other installation is kept rather than risk removing this one.
Function RemovePreviousElsewhere
  ${If} $PrevDir == ""
  ${OrIf} $PrevDir == $INSTDIR
    Return
  ${EndIf}
  ${IfNot} ${FileExists} "$PrevDir\uninstall.exe"
    Return
  ${EndIf}
  Delete "$PrevDir\.downex-setup-same"
  ClearErrors
  FileOpen $0 "$INSTDIR\.downex-setup-same" w
  ${If} ${Errors}
    DetailPrint "Kept the installation in $PrevDir: cannot tell whether it is this folder ($INSTDIR)."
    Return
  ${EndIf}
  FileClose $0
  StrCpy $1 0
  ${If} ${FileExists} "$PrevDir\.downex-setup-same"
    StrCpy $1 1
  ${EndIf}
  Delete "$INSTDIR\.downex-setup-same"
  ${If} $1 = 1
    DetailPrint "$PrevDir is this folder ($INSTDIR)."
    ; The same installation, spelt as $INSTDIR from here on (.onInstFailed compares them).
    StrCpy $PrevDir $INSTDIR
    Return
  ${EndIf}
  DetailPrint "Removing the previous installation in $PrevDir"
  ExecWait '"$PrevDir\uninstall.exe" /S _?=$PrevDir' $0
  ${If} $0 != 0
    DetailPrint "The previous uninstaller ended with $0."
  ${EndIf}
  Delete "$PrevDir\uninstall.exe"
  RMDir "$PrevDir"
FunctionEnd

Function CheckWritable
  ClearErrors
  FileOpen $0 "$INSTDIR\.downex-setup-test" w
  ${If} ${Errors}
    MessageBox MB_OK|MB_ICONSTOP "Setup cannot write to $INSTDIR.$\r$\n$\r$\nChoose a folder in your user profile, such as $LOCALAPPDATA\Programs\Downex." /SD IDOK
    Abort "Cannot write to $INSTDIR."
  ${EndIf}
  FileClose $0
  Delete "$INSTDIR\.downex-setup-test"
FunctionEnd

Function WriteUninstallEntry
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "${PRODUCT}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "${PRODUCT}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\downex-ui.exe,0"
  WriteRegStr HKCU "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKCU "${UNINSTKEY}" "EstimatedSize" "$0"
FunctionEnd

; Starts downexd detached (no console window), with --background when this downexd knows
; that flag, and waits up to 5 seconds for it to answer.
Function StartDaemon
  nsExec::ExecToStack '"$INSTDIR\downexd.exe" --help'
  Pop $0
  Pop $1
  ${StrStr} $2 $1 "-background"
  StrCpy $3 '"$INSTDIR\downexd.exe"'
  ${If} $2 != ""
    StrCpy $3 '$3 --background'
  ${EndIf}
  DetailPrint "Starting downexd"
  ; STARTUPINFOW (cb filled in by &l4) and PROCESS_INFORMATION, zero-initialised.
  System::Call '*(&l4, p, p, p, i, i, i, i, i, i, i, i, &i2, &i2, p, p, p, p) p .r4'
  System::Call '*(p, p, i, i) p .r5'
  System::Call 'kernel32::CreateProcessW(p 0, w r3, p 0, p 0, i 0, i ${DETACH_FLAGS_BREAKAWAY}, p 0, w "$PROFILE", p r4, p r5) i .r0 ? e'
  Pop $6
  ${If} $0 = 0
    System::Call 'kernel32::CreateProcessW(p 0, w r3, p 0, p 0, i 0, i ${DETACH_FLAGS}, p 0, w "$PROFILE", p r4, p r5) i .r0 ? e'
    Pop $6
  ${EndIf}
  ${If} $0 = 0
    DetailPrint "Could not start downexd (Windows error $6); the ${PRODUCT} window starts it."
  ${Else}
    System::Call '*$5(p .r7, p .r8)'
    System::Call 'kernel32::CloseHandle(p r7)'
    System::Call 'kernel32::CloseHandle(p r8)'
  ${EndIf}
  System::Free $4
  System::Free $5
  ${If} $0 = 0
    Return
  ${EndIf}
  ${ForEach} $9 1 20 + 1
    nsExec::Exec '"$INSTDIR\downex.exe" daemon status'
    Pop $0
    ${If} $0 == 0
      DetailPrint "downexd is running."
      Return
    ${EndIf}
    Sleep 250
  ${Next}
  DetailPrint "downexd has not answered yet; see its log in $LOCALAPPDATA\Downex\logs."
FunctionEnd

Section "${PRODUCT} (required)" SecCore
  SectionIn RO
  SetOutPath "$INSTDIR"
  Call CheckWritable
  Call RemovePreviousElsewhere

  ; An upgrade or reinstall: stop the window and the daemon, then set the old programs
  ; aside (from here on a failure puts them back and starts that Downex again:
  ; .onInstFailed). $Upgrade does not rest on the Uninstall entry, which the in-app update's
  ; /D= may not match (missing, another folder, the same one spelt another way).
  StrCpy $R0 "$INSTDIR"
  ${If} ${FileExists} "$INSTDIR\downexd.exe"
    StrCpy $Upgrade 1
  ${EndIf}
  StrCpy $Stopped 1
  Call StopDownexIn
  !insertmacro DELETE_ASIDE_COPIES "$INSTDIR"
  Call SetProgramsAside

  SetOutPath "$INSTDIR"
  File "${SRCDIR}\downexd.exe"
  File "${SRCDIR}\downex.exe"
  File "${SRCDIR}\downex-nmhost.exe"
  File "${SRCDIR}\downex-ui.exe"
  File "/oname=EXTENSION.txt" "${__FILEDIR__}\EXTENSION.md"
  File "/oname=LICENSE.txt" "${__FILEDIR__}\LICENSE.txt"
  File "/oname=THIRD-PARTY-NOTICES.txt" "${__FILEDIR__}\..\ui\THIRD-PARTY-NOTICES.txt"
  ; The extension folders are overwritten in place: a browser may have the extension
  ; loaded from them (EXTENSION.txt), and Setup must not take the folder away under it.
  SetOutPath "$INSTDIR\extension\chrome-mv3"
  File /r "${SRCDIR}\extension\chrome-mv3\*.*"
  SetOutPath "$INSTDIR\extension\firefox-mv3"
  File /r "${SRCDIR}\extension\firefox-mv3\*.*"
  SetOutPath "$INSTDIR"
  Call RemoveUnshippedExtensionFiles
  Call DeleteAside

  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateShortcut "$SMPROGRAMS\${PRODUCT}.lnk" "$INSTDIR\downex-ui.exe" "" "$INSTDIR\downex-ui.exe" 0 SW_SHOWNORMAL "" "${PRODUCT} download manager"
  Call WriteUninstallEntry

  DetailPrint "Registering ${PRODUCT} with the browsers"
  nsExec::ExecToLog '"$INSTDIR\downex-nmhost.exe" register'
  Pop $0
  ${If} $0 != 0
    DetailPrint "downex-nmhost register ended with $0; run $\"$INSTDIR\downex-nmhost.exe$\" register later."
  ${EndIf}

  Call StartDaemon
SectionEnd

Section "Desktop shortcut" SecDesktop
  CreateShortcut "$DESKTOP\${PRODUCT}.lnk" "$INSTDIR\downex-ui.exe" "" "$INSTDIR\downex-ui.exe" 0 SW_SHOWNORMAL "" "${PRODUCT} download manager"
  WriteRegDWORD HKCU "${UNINSTKEY}" "DesktopShortcut" 1
SectionEnd

; An upgrade without the desktop shortcut removes the one an earlier Setup made. The
; choice is recorded for the next Setup (KeepShortcutChoice).
Section "-RemoveDesktopShortcut"
  ${IfNot} ${SectionIsSelected} ${SecDesktop}
    WriteRegDWORD HKCU "${UNINSTKEY}" "DesktopShortcut" 0
    ${If} $PrevDir != ""
      Delete "$DESKTOP\${PRODUCT}.lnk"
    ${EndIf}
  ${EndIf}
SectionEnd

Section /o "Start ${PRODUCT} when I sign in" SecLogin
  ; The daemon writes and checks the sign-in entry itself (DESKTOP-SPEC section 5).
  nsExec::ExecToStack '"$INSTDIR\downex.exe" settings --launch-at-login on'
  Pop $0
  Pop $1
  ${If} $0 == 0
    DetailPrint "${PRODUCT} will start when you sign in."
  ${Else}
    ${StrStr} $2 $1 "not defined"
    ${If} $2 != ""
      DetailPrint "Note: this downex.exe has no --launch-at-login option; skipped. Turn on 'Launch at login' in ${PRODUCT}'s settings instead."
    ${Else}
      DetailPrint "Could not turn on launch at login ($0): $1"
    ${EndIf}
  ${EndIf}
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecCore} "The ${PRODUCT} window, its daemon downexd, the downex command, the browser relay and the browser extension files. Registers ${PRODUCT} with Chrome, Edge, Brave, Chromium and Firefox."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} "A ${PRODUCT} shortcut on the desktop. The Start menu shortcut is always created."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecLogin} "Start the ${PRODUCT} daemon and its notification-area icon when you sign in to Windows. You can change this later in ${PRODUCT}'s settings."
!insertmacro MUI_FUNCTION_DESCRIPTION_END

; Placed after the sections, whose ${Sec...} indexes it uses; .onInit calls it.
; An upgrade keeps the desktop-shortcut choice of the installation it replaces: the one its
; Setup recorded, and the shortcut's absence where no choice is recorded (an older Setup) or
; when the update is silent (the user deleted the shortcut and is not asked again).
Function KeepShortcutChoice
  ${If} $PrevDir == ""
    Return
  ${EndIf}
  ClearErrors
  ReadRegDWORD $0 HKCU "${UNINSTKEY}" "DesktopShortcut"
  ${If} ${Errors}
    StrCpy $0 ""
  ${EndIf}
  StrCpy $1 0
  ${If} $0 == "0"
    StrCpy $1 1
  ${ElseIfNot} ${FileExists} "$DESKTOP\${PRODUCT}.lnk"
    ${If} $0 == ""
    ${OrIf} ${Silent}
      StrCpy $1 1
    ${EndIf}
  ${EndIf}
  ${If} $1 = 1
    !insertmacro UnselectSection ${SecDesktop}
  ${EndIf}
FunctionEnd

; The files of the extension folders this Setup ships (IsShippedExtensionFile, generated
; from SRCDIR\extension while Setup is compiled). Up to three tries: Windows PowerShell,
; started from makensis, has been seen to fail now and then before it ran the script.
; makensis is a 32-bit program, so a bare "powershell" starts the 32-bit Windows PowerShell
; (SysWOW64), whose .NET runtime has been seen to crash at start-up on every try (JIT
; internal limitation, access violation); the native one is used through Sysnative, as
; Setup itself does (PowerShellExe), and "powershell" from PATH only where there is none.
!if /FileExists "$%WINDIR%\Sysnative\WindowsPowerShell\v1.0\powershell.exe"
  !define EXTFILES_PS '$%WINDIR%\Sysnative\WindowsPowerShell\v1.0\powershell.exe'
!else
  !define EXTFILES_PS 'powershell'
!endif
!tempfile EXTFILES_NSH
!macro EXTFILES_TRY
  !ifndef EXTFILES_OK
    !system '${EXTFILES_PS} -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "${__FILEDIR__}\extension-files.ps1" "${SRCDIR}\extension" "${EXTFILES_NSH}"' EXTFILES_RET
    !if "${EXTFILES_RET}" == "0"
      !define EXTFILES_OK
    !endif
    !undef EXTFILES_RET
  !endif
!macroend
!insertmacro EXTFILES_TRY
!insertmacro EXTFILES_TRY
!insertmacro EXTFILES_TRY
!ifndef EXTFILES_OK
  !error "installer\extension-files.ps1 failed three times (its output is above)"
!endif
!undef EXTFILES_OK
!undef EXTFILES_PS
!include "${EXTFILES_NSH}"
!delfile "${EXTFILES_NSH}"
!undef EXTFILES_NSH

; ${Locate} callback: deletes file $R9 of the extension folders unless this Setup ships it.
Function RemoveUnshippedExtensionFile
  StrLen $0 "$INSTDIR\extension\"
  StrCpy $1 $R9 "" $0
  Call IsShippedExtensionFile
  ${If} $2 = 0
    Delete "$R9"
    DetailPrint "Removed extension\$1, which this version no longer ships."
  ${EndIf}
  ClearErrors
  Push ""
FunctionEnd

; ${Locate} callback: removes the empty folder $R9.
Function RemoveEmptyFolder
  RMDir "$R9"
  ClearErrors
  Push ""
FunctionEnd

; Deletes what an earlier version left in the extension folders and this one does not
; ship, then the folders that leaves empty (a few passes, for nested ones).
Function RemoveUnshippedExtensionFiles
  ${Locate} "$INSTDIR\extension\chrome-mv3" "/L=F" RemoveUnshippedExtensionFile
  ${Locate} "$INSTDIR\extension\firefox-mv3" "/L=F" RemoveUnshippedExtensionFile
  ${ForEach} $3 1 4 + 1
    ${Locate} "$INSTDIR\extension\chrome-mv3" "/L=DE" RemoveEmptyFolder
    ${Locate} "$INSTDIR\extension\firefox-mv3" "/L=DE" RemoveEmptyFolder
  ${Next}
  ClearErrors
FunctionEnd

; After a silent install with /RELAUNCH (an in-app update): opens the window again when
; Setup closed it, as the update's text in Settings promises. Interactive Setups offer
; "Start Downex" on the finish page instead.
Function RelaunchWindow
  ${IfNot} ${Silent}
    Return
  ${EndIf}
  ClearErrors
  ${GetOptions} $CMDLINE "/RELAUNCH" $0
  ${If} ${Errors}
    Return
  ${EndIf}
  ${If} ${FileExists} "$PLUGINSDIR\window-closed"
  ${AndIf} ${FileExists} "$INSTDIR\downex-ui.exe"
    DetailPrint "Opening the ${PRODUCT} window again"
    Exec '"$INSTDIR\downex-ui.exe"'
  ${EndIf}
FunctionEnd

Function .onInstSuccess
  Call RelaunchWindow
FunctionEnd

; Setup failed or was cancelled after it stopped Downex: the earlier programs go back in
; place (replacing new files already written), and the daemon and, after an in-app
; update, the window start again, so that the earlier version keeps working.
Function .onInstFailed
  ; Nothing set aside and no Downex in $INSTDIR before: a first installation. An upgrade
  ; that failed before it set anything aside (a program in use) still has its earlier
  ; programs in place, and they only need starting again: after an in-app update nothing
  ; else would, as its daemon has already exited.
  ${If} $Stopped != 1
    Return
  ${EndIf}
  ${If} "$AsideNmhost$AsideCli$AsideDaemon$AsideUI" == ""
  ${AndIf} $Upgrade != 1
  ${AndIf} $PrevDir != $INSTDIR
    Return
  ${EndIf}
  StrCpy $R2 "$INSTDIR\downex-nmhost.exe"
  StrCpy $R3 $AsideNmhost
  Call RestoreAside
  StrCpy $R2 "$INSTDIR\downex.exe"
  StrCpy $R3 $AsideCli
  Call RestoreAside
  StrCpy $R2 "$INSTDIR\downexd.exe"
  StrCpy $R3 $AsideDaemon
  Call RestoreAside
  StrCpy $R2 "$INSTDIR\downex-ui.exe"
  StrCpy $R3 $AsideUI
  Call RestoreAside
  ${If} ${FileExists} "$INSTDIR\downexd.exe"
  ${AndIf} ${FileExists} "$INSTDIR\downex.exe"
    Call StartDaemon
  ${EndIf}
  Call RelaunchWindow
FunctionEnd

; ---------------------------------------------------------------------------------------
; Uninstaller

; Deletes file $R2, which may still be in use for a moment by a program that is exiting
; (or that a browser started again): retries for 10 seconds, stops the Downex programs in
; $R0 again halfway, then asks to retry or cancel. Silent: cancels.
Function un.RemoveFile
  Push $R3
  StrCpy $R3 0
  ${Do}
    ${IfNot} ${FileExists} "$R2"
      ${Break}
    ${EndIf}
    Delete "$R2"
    ${IfNot} ${FileExists} "$R2"
      ${Break}
    ${EndIf}
    IntOp $R3 $R3 + 1
    ${If} $R3 = 10
      Call un.StopDownexIn
    ${ElseIf} $R3 >= 20
      MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "$R2 is in use.$\r$\n$\r$\nClose ${PRODUCT} and any browser window whose ${PRODUCT} extension is busy, then click Retry." /SD IDCANCEL IDRETRY retry
      DetailPrint "$R2 is in use; cancelled."
      Abort "$R2 is in use."
      retry:
      StrCpy $R3 0
      Call un.StopDownexIn
    ${EndIf}
    Sleep 500
  ${Loop}
  Pop $R3
FunctionEnd

; Deletes the program files of the installation in $R0 (retrying while they are in use):
; the relay first, which a browser may start again, and the window last, so that an
; uninstaller that gives up leaves a window to start Downex with.
Function un.RemovePrograms
  Push $R2
  StrCpy $R2 "$R0\downex-nmhost.exe"
  Call un.RemoveFile
  StrCpy $R2 "$R0\downex.exe"
  Call un.RemoveFile
  StrCpy $R2 "$R0\downexd.exe"
  Call un.RemoveFile
  StrCpy $R2 "$R0\downex-ui.exe"
  Call un.RemoveFile
  Pop $R2
FunctionEnd

; Removes the sign-in entry when it starts this installation's downexd.
Function un.RemoveRunEntry
  ReadRegStr $0 HKCU "${RUNKEY}" "${RUNVALUE}"
  ${If} $0 == ""
    Return
  ${EndIf}
  ${ForEach} $3 1 2 + 1
    ${If} $3 = 1
      StrCpy $1 '"$INSTDIR\downexd.exe"'
    ${Else}
      StrCpy $1 '$INSTDIR\downexd.exe'
    ${EndIf}
    StrLen $2 $1
    StrCpy $2 $0 $2
    ${If} $2 == $1   ; case-insensitive
      DeleteRegValue HKCU "${RUNKEY}" "${RUNVALUE}"
      DetailPrint "Removed the sign-in entry ${RUNVALUE}."
      Return
    ${EndIf}
  ${Next}
  DetailPrint "Kept the sign-in entry ${RUNVALUE}: it starts $0, not this installation."
FunctionEnd

Section "Uninstall"
  ; Only the installation that the Uninstall key names owns the shortcuts, the browser
  ; registration and the Uninstall key; a leftover copy elsewhere removes just its files.
  ReadRegStr $0 HKCU "${UNINSTKEY}" "InstallLocation"
  StrCpy $Registered 0
  ${If} $0 == ""
  ${OrIf} $0 == $INSTDIR
    StrCpy $Registered 1
  ${EndIf}

  StrCpy $R0 "$INSTDIR"
  Call un.StopDownexIn

  ${If} $Registered = 1
  ${AndIf} ${FileExists} "$INSTDIR\downex-nmhost.exe"
    DetailPrint "Removing the browser registration"
    nsExec::ExecToLog '"$INSTDIR\downex-nmhost.exe" unregister'
    Pop $0
    ${If} $0 != 0
      DetailPrint "downex-nmhost unregister ended with $0."
    ${EndIf}
  ${EndIf}
  Call un.RemoveRunEntry
  ${If} $Registered = 1
    Delete "$SMPROGRAMS\${PRODUCT}.lnk"
    Delete "$DESKTOP\${PRODUCT}.lnk"
  ${EndIf}

  Call un.RemovePrograms
  ; Programs an earlier Setup set aside while they were in use (none runs any more now).
  !insertmacro DELETE_ASIDE_COPIES "$INSTDIR"
  Delete "$INSTDIR\EXTENSION.txt"
  Delete "$INSTDIR\LICENSE.txt"
  Delete "$INSTDIR\THIRD-PARTY-NOTICES.txt"
  RMDir /r "$INSTDIR\extension\chrome-mv3"
  RMDir /r "$INSTDIR\extension\firefox-mv3"
  RMDir "$INSTDIR\extension"
  ; Setup's own marker files, should one have been ended before it deleted them.
  Delete "$INSTDIR\.downex-setup-same"
  Delete "$INSTDIR\.downex-setup-test"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  ${If} $Registered = 1
    DeleteRegKey HKCU "${UNINSTKEY}"
    MessageBox MB_YESNO|MB_ICONQUESTION|MB_DEFBUTTON2 "Also delete your ${PRODUCT} data: the download list, settings, logs and the yt-dlp and Deno copies it downloaded in$\r$\n$APPDATA\Downex$\r$\n$LOCALAPPDATA\Downex ?$\r$\n$\r$\nDownloaded files are kept either way." /SD IDNO IDNO keepdata
      RMDir /r "$APPDATA\Downex"
      ; The yt-dlp and Deno copies of site media (docs/SITE-MEDIA-SPEC.md section 3.9) are named on
      ; their own: they are programs, and downexd, which ran them (and with it every child
      ; it started), has stopped by now.
      RMDir /r "$LOCALAPPDATA\Downex\tools"
      RMDir /r "$LOCALAPPDATA\Downex"
      DetailPrint "Deleted the ${PRODUCT} data."
    keepdata:
  ${EndIf}
SectionEnd

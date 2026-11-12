; Herakles setup for Windows (NSIS 3). Built by packaging/windows/build.sh:
;
;   makensis -DVERSION=0.9.1 -DAMD64=herakles-windows-amd64.exe \
;            -DARM64=herakles-windows-arm64.exe -DOUTFILE=herakles-setup.exe herakles.nsi
;
; One setup for x64 and ARM64 Windows. It asks how Herakles is used and
; leaves the rest to "herakles windows-setup" (internal/cli/cmd_winsetup_windows.go).
;
; Unattended (deployment tools, servers):
;   herakles-setup.exe /S [/MODE=desktop|user|none] [/HUB=HKE1-...] [/NAME=web-1]
;                         [/USER=DOMAIN\name] [/NOAUTOSTART] [/NOPATH] [/D=C:\path]
; Uninstall unattended: "C:\Program Files\Herakles\uninstall.exe" /S [/PURGE]

Unicode true
ManifestDPIAware true
RequestExecutionLevel admin
SetCompressor /SOLID lzma

!include MUI2.nsh
!include nsDialogs.nsh
!include LogicLib.nsh
!include FileFunc.nsh
!include x64.nsh
!include WinMessages.nsh

!ifndef VERSION
  !error "-DVERSION=X.Y.Z is required"
!endif

!define PRODUCT "Herakles Backup"
!define REGKEY "Software\Herakles"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Herakles"
!define WEBSITE "https://herakles.kloeck-it.de"

Name "${PRODUCT}"
OutFile "${OUTFILE}"
InstallDir "$PROGRAMFILES64\Herakles"
BrandingText "Herakles ${VERSION}"
VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${PRODUCT}"
VIAddVersionKey "FileDescription" "${PRODUCT} setup"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "Herakles authors"

!define MUI_ICON "herakles.ico"
!define MUI_UNICON "herakles.ico"
!define MUI_WELCOMEFINISHPAGE_BITMAP "wizard.bmp"
!define MUI_UNWELCOMEFINISHPAGE_BITMAP "wizard.bmp"
!define MUI_ABORTWARNING
!define MUI_WELCOMEPAGE_TEXT "This installs Herakles ${VERSION}: deduplicated, compressed and encrypted backups of files, folders and whole disks.$\r$\n$\r$\nNext you choose how Herakles runs on this computer. The backup wizard in the Herakles window then sets up your first backup."

Var Upgrade      ; 1 when Herakles is already installed: only replace the files
Var Mode         ; desktop | user | none
Var HubCode
Var HubName
Var SetupUser
Var Autostart    ; 1 / 0
Var AddPath      ; 1 / 0
Var Purge        ; uninstaller: 1 deletes the service's configuration

; mode page controls
Var RDesktop
Var RUser
Var RNone
Var HubLabel
Var HubText
Var CAuto
Var CPath

!insertmacro MUI_PAGE_WELCOME
Page custom ModePage ModeLeave
!define MUI_PAGE_CUSTOMFUNCTION_PRE SkipOnUpgrade
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "Open Herakles now"
!define MUI_FINISHPAGE_RUN_FUNCTION OpenHerakles
!define MUI_FINISHPAGE_LINK "Guide: ${WEBSITE}/guide.html"
!define MUI_FINISHPAGE_LINK_LOCATION "${WEBSITE}/guide.html"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_COMPONENTS
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; ------------------------------------------------------------------ setup

Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "Herakles needs 64-bit Windows (x64 or ARM64)." /SD IDOK
    Abort
  ${EndIf}
  SetRegView 64
  StrCpy $Mode "desktop"
  StrCpy $Autostart 1
  StrCpy $AddPath 1
  ReadRegStr $0 HKLM "${REGKEY}" "InstallDir"
  ${If} $0 != ""
  ${AndIf} ${FileExists} "$0\herakles.exe"
    StrCpy $Upgrade 1
    StrCpy $INSTDIR $0
  ${EndIf}

  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/MODE=" $1
  ${IfNot} ${Errors}
    StrCpy $Mode $1
  ${EndIf}
  ${GetOptions} $R0 "/HUB=" $HubCode
  ${GetOptions} $R0 "/NAME=" $HubName
  ${GetOptions} $R0 "/USER=" $SetupUser
  ClearErrors
  ${GetOptions} $R0 "/NOAUTOSTART" $1
  ${IfNot} ${Errors}
    StrCpy $Autostart 0
  ${EndIf}
  ClearErrors
  ${GetOptions} $R0 "/NOPATH" $1
  ${IfNot} ${Errors}
    StrCpy $AddPath 0
  ${EndIf}
  ClearErrors
FunctionEnd

Function SkipOnUpgrade
  ${If} $Upgrade == 1
    Abort
  ${EndIf}
FunctionEnd

Function ModePage
  ${If} $Upgrade == 1
    Abort
  ${EndIf}
  !insertmacro MUI_HEADER_TEXT "How will you use Herakles?" "You can change this later in Herakles."
  nsDialogs::Create 1018
  Pop $0

  ${NSD_CreateRadioButton} 0 0 100% 10u "&Whole computer (recommended)"
  Pop $RDesktop
  ${NSD_CreateLabel} 12u 11u -12u 18u "Backups run in a Windows service: every file and whole disks, also when nobody is logged in. The Herakles window opens the service."
  Pop $0
  ${NSD_CreateRadioButton} 0 31u 100% 10u "&Only my own files"
  Pop $RUser
  ${NSD_CreateLabel} 12u 42u -12u 10u "Herakles runs as you, while you are logged in."
  Pop $0
  ${NSD_CreateRadioButton} 0 54u 100% 10u "&Install only"
  Pop $RNone

  ${NSD_CreateLabel} 0 70u 100% 10u "&Hub enrollment code (optional; attaches this computer to a Herakles hub):"
  Pop $HubLabel
  ${NSD_CreateText} 0 81u 100% 12u $HubCode
  Pop $HubText
  SendMessage $HubText ${EM_SETCUEBANNER} 1 "STR:HKE1-…  from the hub's Servers page"

  ${NSD_CreateCheckbox} 0 99u 100% 10u "Show the Herakles icon in the &tray when I log in"
  Pop $CAuto
  ${NSD_CreateCheckbox} 0 111u 100% 10u "Add Herakles to the &PATH (for the command line)"
  Pop $CPath

  ${NSD_OnClick} $RDesktop ModeChanged
  ${NSD_OnClick} $RUser ModeChanged
  ${NSD_OnClick} $RNone ModeChanged
  ${If} $Mode == "user"
    ${NSD_Check} $RUser
  ${ElseIf} $Mode == "none"
    ${NSD_Check} $RNone
  ${Else}
    ${NSD_Check} $RDesktop
  ${EndIf}
  ${If} $Autostart == 1
    ${NSD_Check} $CAuto
  ${EndIf}
  ${If} $AddPath == 1
    ${NSD_Check} $CPath
  ${EndIf}
  Call ModeChanged
  nsDialogs::Show
FunctionEnd

Function ModeChanged
  Pop $0 ; the clicked control, when called as a handler (harmless otherwise)
  ${NSD_GetState} $RDesktop $1
  EnableWindow $HubLabel $1
  EnableWindow $HubText $1
  ${NSD_GetState} $RNone $2
  ${If} $2 == ${BST_CHECKED}
    EnableWindow $CAuto 0
  ${Else}
    EnableWindow $CAuto 1
  ${EndIf}
FunctionEnd

Function ModeLeave
  ${NSD_GetState} $RUser $0
  ${NSD_GetState} $RNone $1
  ${If} $0 == ${BST_CHECKED}
    StrCpy $Mode "user"
  ${ElseIf} $1 == ${BST_CHECKED}
    StrCpy $Mode "none"
  ${Else}
    StrCpy $Mode "desktop"
  ${EndIf}
  ${NSD_GetText} $HubText $HubCode
  ${If} $Mode != "desktop"
    StrCpy $HubCode ""
  ${EndIf}
  ${If} $HubCode != ""
    StrCpy $0 $HubCode 5
    ${If} $0 != "HKE1-"
      MessageBox MB_ICONEXCLAMATION "An enrollment code starts with HKE1-. Copy it from the hub: Servers → Enroll a server."
      Abort
    ${EndIf}
  ${EndIf}
  ${NSD_GetState} $CAuto $Autostart
  ${NSD_GetState} $CPath $AddPath
FunctionEnd

; Runs herakles.exe hidden; shows its output in the details and, on
; failure, in a message box.
!macro HeraklesRun ARGS WHAT
  nsExec::ExecToStack '"$INSTDIR\herakles.exe" ${ARGS}'
  Pop $0
  Pop $1
  DetailPrint "$1"
  ${If} $0 != 0
    MessageBox MB_ICONEXCLAMATION "${WHAT} did not work:$\r$\n$\r$\n$1" /SD IDOK
    SetErrorLevel 2
  ${EndIf}
!macroend

Section "Herakles" SecMain
  SectionIn RO
  SetRegView 64
  SetShellVarContext all
  SetOutPath "$INSTDIR"

  ; A running herakles.exe (tray icons, the service) cannot be overwritten
  ; but can be renamed; the old file goes on the next update.
  Delete "$INSTDIR\herakles.exe.old"
  ${If} ${FileExists} "$INSTDIR\herakles.exe"
    Rename "$INSTDIR\herakles.exe" "$INSTDIR\herakles.exe.old"
  ${EndIf}
  ${If} ${IsNativeARM64}
    File "/oname=herakles.exe" "${ARM64}"
  ${Else}
    File "/oname=herakles.exe" "${AMD64}"
  ${EndIf}
  File "herakles.ico"
  Delete "$INSTDIR\herakles.exe.old"

  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateShortcut "$SMPROGRAMS\Herakles.lnk" "$INSTDIR\herakles.exe" "tray --open" "$INSTDIR\herakles.ico" 0 SW_SHOWMINIMIZED "" "Back up and restore with Herakles"

  WriteRegStr HKLM "${REGKEY}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${REGKEY}" "Version" "${VERSION}"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayName" "${PRODUCT}"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\herakles.ico"
  WriteRegStr HKLM "${UNINSTKEY}" "Publisher" "Herakles"
  WriteRegStr HKLM "${UNINSTKEY}" "URLInfoAbout" "${WEBSITE}"
  WriteRegStr HKLM "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKLM "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKLM "${UNINSTKEY}" "EstimatedSize" "$0"

  ${If} $Upgrade == 1
    !insertmacro HeraklesRun "windows-setup restart" "Restarting the Herakles service"
    Return
  ${EndIf}

  StrCpy $R1 "windows-setup install --mode $Mode"
  ${If} $Autostart == 0
    StrCpy $R1 "$R1 --autostart=false"
  ${EndIf}
  ${If} $AddPath == 1
    StrCpy $R1 "$R1 --path"
  ${EndIf}
  ${If} $SetupUser != ""
    StrCpy $R1 '$R1 --user "$SetupUser"'
  ${EndIf}
  ${If} $HubCode != ""
    StrCpy $R1 '$R1 --hub "$HubCode"'
  ${EndIf}
  ${If} $HubName != ""
    StrCpy $R1 '$R1 --name "$HubName"'
  ${EndIf}
  DetailPrint "Setting up Herakles ($Mode)…"
  !insertmacro HeraklesRun "$R1" "Setting up Herakles"
  WriteRegStr HKLM "${REGKEY}" "Mode" "$Mode"
SectionEnd

; Started through Explorer, so Herakles runs as the logged-in user and not
; with the installer's administrator rights.
Function OpenHerakles
  Exec '"$WINDIR\explorer.exe" "$SMPROGRAMS\Herakles.lnk"'
FunctionEnd

; ------------------------------------------------------------------ uninstall

Function un.onInit
  SetRegView 64
  StrCpy $Purge 0
  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/PURGE" $1
  ${IfNot} ${Errors}
    StrCpy $Purge 1
  ${EndIf}
FunctionEnd

Section "un.Herakles" UnMain
  SectionIn RO
SectionEnd

Section /o "un.Delete the service's configuration and logs" UnPurge
  StrCpy $Purge 1
SectionEnd

; Runs last, after the choices above.
Section "-un.Remove"
  SetRegView 64
  SetShellVarContext all
  ${If} $Purge == 1
    StrCpy $R1 "windows-setup uninstall --purge"
  ${Else}
    StrCpy $R1 "windows-setup uninstall"
  ${EndIf}
  nsExec::ExecToStack '"$INSTDIR\herakles.exe" $R1'
  Pop $0
  Pop $1
  DetailPrint "$1"
  ; Tray icons of logged-in users still run the program.
  nsExec::Exec '"$SYSDIR\taskkill.exe" /F /IM herakles.exe'
  Pop $0
  Sleep 500

  Delete "$SMPROGRAMS\Herakles.lnk"
  Delete "$INSTDIR\herakles.exe"
  Delete "$INSTDIR\herakles.exe.old"
  Delete "$INSTDIR\herakles.ico"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKLM "${UNINSTKEY}"
  DeleteRegKey HKLM "${REGKEY}"
SectionEnd

!insertmacro MUI_UNFUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${UnMain} "Removes the program, the Windows service, the tray autostart and the PATH entry."
  !insertmacro MUI_DESCRIPTION_TEXT ${UnPurge} "Also deletes %ProgramData%\Herakles (configuration with repository passwords, logs, caches). Your backups in the repositories are not touched."
!insertmacro MUI_UNFUNCTION_DESCRIPTION_END

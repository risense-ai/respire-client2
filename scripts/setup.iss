; Respire Windows installer.
; Compiled by scripts/build-installer.ps1 via ISCC with defines:
;   /DAppVersion=1.2.3  /DArch=amd64|arm64  /DOutputBaseName=respire-windows-<arch>-setup
; Requires dist/respire-windows-<arch>.exe, dist/LICENSE.txt, dist/THIRD_PARTY_NOTICES.txt
; (from scripts/build.ps1) and dist/bootstrap (from scripts/build-installer.ps1).

#ifndef AppVersion
#define AppVersion "0.0.0-dev"
#endif
#ifndef Arch
#define Arch "amd64"
#endif
#ifndef OutputBaseName
#define OutputBaseName "respire-windows-" + Arch + "-setup"
#endif
#define AppGuid "AFCA612B-7DD5-4380-8DD2-ADE8E2DA28C4"
; Braces must come from the define value: a literal "{{#AppGuid}" gets
; mangled by ISPP brace escaping.
#define AppIdValue "{{" + AppGuid + "}"
; VERSIONINFO resources need a numeric version; strip any prerelease suffix.
#define Dash Pos("-", AppVersion)
#if Dash > 0
#define NumVersion Copy(AppVersion, 1, Dash - 1) + ".0"
#else
#define NumVersion AppVersion + ".0"
#endif

[Setup]
AppId={#AppIdValue}
AppName=Respire
AppVersion={#AppVersion}
AppVerName=Respire {#AppVersion}
AppPublisher=Risense AI
DefaultDirName={localappdata}\Programs\Respire
PrivilegesRequired=lowest
MinVersion=0,10.0
WizardStyle=modern
DisableProgramGroupPage=yes
AllowNoIcons=yes
Compression=lzma2/max
SolidCompression=yes
OutputDir=..\dist
OutputBaseFilename={#OutputBaseName}
VersionInfoVersion={#NumVersion}
VersionInfoProductVersion={#NumVersion}
VersionInfoProductName=Respire
VersionInfoCompany=Risense AI
VersionInfoDescription=Respire Windows installer
VersionInfoCopyright=Copyright 2026 Respire contributors
UninstallDisplayName=Respire
UninstallDisplayIcon={app}\Respire.exe
SetupLogging=yes

[Languages]
Name: "en"; MessagesFile: "compiler:Default.isl"
Name: "zh"; MessagesFile: "innosetup\ChineseSimplified.isl"

[CustomMessages]
en.AddPathTask=Add the rsrs command-line tool to your PATH (recommended)
zh.AddPathTask=将 rsrs 命令行工具加入用户 PATH（推荐）
en.WebView2ManualNote=Microsoft Edge WebView2 Runtime could not be installed automatically. Please install it from the official page (developer.microsoft.com/microsoft-edge/webview2) before starting Respire.
zh.WebView2ManualNote=未能自动安装 Microsoft Edge WebView2 Runtime。请先访问官方页面（developer.microsoft.com/microsoft-edge/webview2）手动安装，再启动 Respire。

[Files]
Source: "..\dist\respire-windows-{#Arch}.exe"; DestDir: "{app}"; DestName: "Respire.exe"; Flags: ignoreversion
Source: "..\dist\LICENSE.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\dist\THIRD_PARTY_NOTICES.txt"; DestDir: "{app}"; Flags: ignoreversion
; User data and receipts are published by the application's verified bootstrap
; transaction on first launch, never by the installer.
Source: "..\dist\bootstrap\*"; DestDir: "{app}\bootstrap"; Flags: recursesubdirs createallsubdirs ignoreversion

[Tasks]
Name: "addpath"; Description: "{cm:AddPathTask}"

[Icons]
Name: "{autoprograms}\Respire"; Filename: "{app}\Respire.exe"; Check: not WizardNoIcons
Name: "{autodesktop}\Respire"; Filename: "{app}\Respire.exe"; Check: not WizardNoIcons

[Run]
Filename: "{app}\Respire.exe"; Description: "{cm:LaunchProgram,Respire}"; Flags: nowait postinstall skipifsilent runasoriginaluser

[Code]
const
  WebView2MinVersion = '86.0.622.0';
  // Official Evergreen Bootstrapper link (MicrosoftEdgeWebview2Setup.exe).
  WebView2BootstrapperURL = 'https://go.microsoft.com/fwlink/?linkid=2124703';
  WebView2ClientsKey = 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}';

var
  WebView2Failed: Boolean;
  UninstallPathAdded: Boolean;

{ --- small helpers --------------------------------------------------------- }

function NextNum(var S: String): Int64;
var
  P: Integer; T: String;
begin
  P := Pos('.', S);
  if P > 0 then begin
    T := Copy(S, 1, P - 1);
    S := Copy(S, P + 1, MaxInt);
  end else begin
    T := S;
    S := '';
  end;
  Result := StrToIntDef(Trim(T), 0);
end;

{ <0 if A<B, 0 if equal, >0 if A>B }
function CompareVersion(A, B: String): Integer;
var
  NA, NB: Int64;
begin
  Result := 0;
  while (Result = 0) and ((A <> '') or (B <> '')) do begin
    NA := NextNum(A);
    NB := NextNum(B);
    if NA < NB then
      Result := -1
    else if NA > NB then
      Result := 1;
  end;
end;

procedure BroadcastSettingChange;
begin
  { HWND_BROADCAST ($FFFF), WM_SETTINGCHANGE ($001A) }
  SendMessage($FFFF, $001A, 0, 0);
end;

{ --- WebView2 -------------------------------------------------------------- }

function WebView2Installed(): Boolean;
var
  Pv: String;
begin
  Result := False;
  if RegQueryStringValue(HKLM, 'SOFTWARE\WOW6432Node\' + WebView2ClientsKey, 'pv', Pv) or
     RegQueryStringValue(HKLM, WebView2ClientsKey, 'pv', Pv) or
     RegQueryStringValue(HKCU, WebView2ClientsKey, 'pv', Pv) then
    Result := (Trim(Pv) <> '') and (CompareVersion(Trim(Pv), WebView2MinVersion) >= 0);
end;

{ --- PATH ------------------------------------------------------------------ }

function PathEntry(): String;
begin
  Result := ExpandConstant('{%USERPROFILE}') + '\.rsrs\bin';
end;

procedure AddUserPath;
var
  Path, NewPath, Entry: String;
begin
  if not WizardIsTaskSelected('addpath') then
    Exit;
  Entry := PathEntry();
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', Path) then
    Path := '';
  if Pos(';' + Lowercase(Entry) + ';', ';' + Lowercase(Trim(Path)) + ';') > 0 then begin
    { Keep any ownership marker from an earlier installation. }
    Log('PATH already contains ' + Entry + '; retaining its ownership marker');
    Exit;
  end;
  NewPath := Trim(Path);
  if NewPath = '' then
    NewPath := Entry
  else
    NewPath := NewPath + ';' + Entry;
  if not RegWriteExpandStringValue(HKCU, 'Environment', 'Path', NewPath) then
    RaiseException('Cannot update user PATH');
  if not RegWriteDWordValue(HKCU, 'Software\Respire\Installer', 'PathAdded', 1) then
    RaiseException('Cannot record user PATH ownership');
  BroadcastSettingChange;
  Log('added ' + Entry + ' to user PATH');
end;

procedure RemoveUserPath;
var
  Path, Rest, Piece, NewPath, Entry: String;
  P: Integer;
  Found: Boolean;
begin
  if not UninstallPathAdded then
    Exit;
  Entry := Lowercase(PathEntry());
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', Path) then begin
    RegDeleteValue(HKCU, 'Software\Respire\Installer', 'PathAdded');
    Exit;
  end;
  Found := False;
  NewPath := '';
  Rest := Path;
  while Rest <> '' do begin
    P := Pos(';', Rest);
    if P > 0 then begin
      Piece := Copy(Rest, 1, P - 1);
      Rest := Copy(Rest, P + 1, MaxInt);
    end else begin
      Piece := Rest;
      Rest := '';
    end;
    Piece := Trim(Piece);
    if Piece <> '' then begin
      if Lowercase(Piece) = Entry then
        Found := True
      else begin
        if NewPath <> '' then
          NewPath := NewPath + ';';
        NewPath := NewPath + Piece;
      end;
    end;
  end;
  if not Found then begin
    RegDeleteValue(HKCU, 'Software\Respire\Installer', 'PathAdded');
    Exit;
  end;
  if not RegWriteExpandStringValue(HKCU, 'Environment', 'Path', NewPath) then
    RaiseException('Cannot update user PATH');
  if not RegDeleteValue(HKCU, 'Software\Respire\Installer', 'PathAdded') then
    RaiseException('Cannot clear user PATH ownership');
  BroadcastSettingChange;
  Log('removed ' + PathEntry() + ' from user PATH');
end;

{ --- lifecycle -------------------------------------------------------------- }

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  RC: Integer;
begin
  Result := '';
  if WebView2Installed() then begin
    Log('WebView2 Runtime present');
    Exit;
  end;
  Log('WebView2 Runtime missing; running bootstrapper');
  try
    DownloadTemporaryFile(WebView2BootstrapperURL, 'MicrosoftEdgeWebview2Setup.exe', '', nil);
    if Exec(ExpandConstant('{tmp}\MicrosoftEdgeWebview2Setup.exe'), '/silent /install',
      '', SW_HIDE, ewWaitUntilTerminated, RC) and (RC = 0) then
      Log('WebView2 bootstrapper succeeded')
    else begin
      WebView2Failed := True;
      Log(Format('WebView2 bootstrapper failed (rc=%d); install continues', [RC]));
    end;
  except
    WebView2Failed := True;
    Log('WebView2 download failed; install continues');
  end;
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then begin
    AddUserPath;
    if WebView2Failed then begin
      WizardForm.FinishedLabel.Caption := ExpandConstant('{cm:WebView2ManualNote}');
      WizardForm.FinishedLabel.Height := ScaleY(60);
    end;
  end;
end;

function InitializeUninstall(): Boolean;
var
  Marker: Cardinal;
begin
  Result := True;
  UninstallPathAdded := RegQueryDwordValue(HKCU, 'Software\Respire\Installer', 'PathAdded', Marker) and (Marker = 1);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usUninstall then
    RemoveUserPath;
end;

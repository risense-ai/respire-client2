; Respire Windows installer.
; Compiled by scripts/build-installer.ps1 via ISCC with defines:
;   /DAppVersion=1.2.3  /DArch=amd64|arm64  /DCliVersion=1.0.20  /DOutputBaseName=respire-windows-<arch>-setup
; Requires dist/respire-windows-<arch>.exe, dist/LICENSE.txt, dist/THIRD_PARTY_NOTICES.txt
; (from scripts/build.ps1) and dist/cli/rsrs.exe + dist/cli/version.txt (from scripts/fetch-cli.ps1).

#ifndef AppVersion
#define AppVersion "0.0.0-dev"
#endif
#ifndef Arch
#define Arch "amd64"
#endif
#ifndef OutputBaseName
#define OutputBaseName "respire-windows-" + Arch + "-setup"
#endif
#ifndef RtVersion
#define RtVersion "0.0.0"
#endif
#ifndef RtSha256
#define RtSha256 ""
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
DefaultDirName={autopf}\Respire
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
MinVersion=0,10.0
WizardStyle=modern
DisableProgramGroupPage=yes
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
; Staged to {tmp}; [Code] installs it to {%USERPROFILE}\.rsrs\bin with a no-downgrade check.
Source: "..\dist\cli\rsrs.exe"; DestDir: "{tmp}"; Flags: deleteafterinstall
; Managed Runtime, pre-staged into the client's managed layout so the first
; launch skips its download. uninsneveruninstall: the app owns this tree.
Source: "..\dist\runtime\{#RtVersion}\*"; DestDir: "{%USERPROFILE}\.rsrs\client2\runtime\{#RtVersion}"; Flags: recursesubdirs createallsubdirs uninsneveruninstall ignoreversion; Check: ShouldInstallRuntime

[Tasks]
Name: "addpath"; Description: "{cm:AddPathTask}"

[Icons]
Name: "{autoprograms}\Respire"; Filename: "{app}\Respire.exe"
Name: "{autodesktop}\Respire"; Filename: "{app}\Respire.exe"

; Marker proving THIS install added the PATH entry; the uninstaller removes the
; entry only when this marker exists (it is cleared when the entry already existed).
[Registry]
Root: HKCU; Subkey: "Software\Respire\Installer"; ValueType: dword; ValueName: "PathAdded"; ValueData: "1"; Tasks: addpath; Flags: uninsdeletekey noerror

[Run]
Filename: "{app}\Respire.exe"; Description: "{cm:LaunchProgram,Respire}"; Flags: nowait postinstall skipifsilent

[Code]
const
  WebView2MinVersion = '86.0.622.0';
  // Official Evergreen Bootstrapper link (MicrosoftEdgeWebview2Setup.exe).
  WebView2BootstrapperURL = 'https://go.microsoft.com/fwlink/?linkid=2124703';
  WebView2ClientsKey = 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FDF2-4B09-9EB3-A7EE8C27C5C8}';

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

{ second whitespace token, e.g. "rsrs 1.0.20" -> "1.0.20"; '' if absent }
function SecondToken(const S: String): String;
var
  P: Integer;
begin
  Result := Trim(S);
  P := Pos(' ', Result);
  if P > 0 then
    Result := Trim(Copy(Result, P + 1, MaxInt))
  else
    Result := '';
end;

{ runs "<exe> --version" and returns its first line; '' on failure }
function GetExeFirstLine(const Exe, WorkDir: String): String;
var
  RC: Integer;
  OutFile: String;
  Lines: TArrayOfString;
begin
  Result := '';
  OutFile := WorkDir + '\rsrsversion.txt';
  if Exec(ExpandConstant('{cmd}'),
    Format('/C ""%s" --version > "%s" 2>nul"', [Exe, OutFile]),
    '', SW_HIDE, ewWaitUntilTerminated, RC) then
    if LoadStringsFromFile(OutFile, Lines) and (Length(Lines) > 0) then
      Result := Trim(Lines[0]);
  DeleteFile(OutFile);
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

{ --- rsrs.exe: place into the user data bin, never downgrading ------------- }

procedure InstallCli;
var
  CliSrc, CliDst, DstDir, NewVer, ExistingVer: String;
begin
  CliSrc := ExpandConstant('{tmp}\rsrs.exe');
  if not FileExists(CliSrc) then begin
    Log('rsrs.exe not staged; skipping CLI install');
    Exit;
  end;
  DstDir := ExpandConstant('{%USERPROFILE}') + '\.rsrs\bin';
  CliDst := DstDir + '\rsrs.exe';
  NewVer := SecondToken(GetExeFirstLine(CliSrc, ExpandConstant('{tmp}')));
  if FileExists(CliDst) then begin
    ExistingVer := SecondToken(GetExeFirstLine(CliDst, ExpandConstant('{tmp}')));
    if (ExistingVer <> '') and (NewVer <> '') and (CompareVersion(NewVer, ExistingVer) < 0) then begin
      Log(Format('existing rsrs %s is newer than bundled %s; keeping it', [ExistingVer, NewVer]));
      Exit;
    end;
  end;
  if not ForceDirectories(DstDir) then begin
    Log('cannot create ' + DstDir);
    Exit;
  end;
  if not CopyFile(CliSrc, CliDst, False) then
    Log('failed to copy rsrs.exe to ' + DstDir)
  else
    Log(Format('installed rsrs %s to %s', [NewVer, CliDst]));
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
    { entry already exists - not ours to remove later; drop the marker }
    Log('PATH already contains ' + Entry + '; clearing PathAdded marker');
    RegDeleteValue(HKCU, 'Software\Respire\Installer', 'PathAdded');
    Exit;
  end;
  NewPath := Trim(Path);
  if NewPath = '' then
    NewPath := Entry
  else
    NewPath := NewPath + ';' + Entry;
  RegWriteExpandStringValue(HKCU, 'Environment', 'Path', NewPath);
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
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', Path) then
    Exit;
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
  if not Found then
    Exit;
  RegWriteExpandStringValue(HKCU, 'Environment', 'Path', NewPath);
  BroadcastSettingChange;
  Log('removed ' + PathEntry() + ' from user PATH');
end;

{ --- managed Runtime pre-staging -------------------------------------------- }

function RuntimeStateFile(): String;
begin
  Result := ExpandConstant('{%USERPROFILE}') + '\.rsrs\client2\runtime.json';
end;

{ reads the "version" value from the client's runtime.json; '' if unreadable }
function ExistingRuntimeVersion(): String;
var
  Lines: TArrayOfString;
  S: String;
  I, P, Q: Integer;
begin
  Result := '';
  if not LoadStringsFromFile(RuntimeStateFile(), Lines) then
    Exit;
  for I := 0 to GetArrayLength(Lines) - 1 do begin
    S := Lines[I];
    P := Pos('"version"', S);
    if P = 0 then
      Continue;
    while (P <= Length(S)) and (Copy(S, P, 1) <> ':') do
      P := P + 1;
    while (P <= Length(S)) and (Copy(S, P, 1) <> '"') do
      P := P + 1;
    Q := P + 1;
    while (Q <= Length(S)) and (Copy(S, Q, 1) <> '"') do
      Q := Q + 1;
    if (P <= Length(S)) and (Q <= Length(S)) then
      Result := Copy(S, P + 1, Q - P - 1);
    Exit;
  end;
end;

{ PascalScript has no nested functions; these helpers live at top level }
function SemverNum(var S: String): Integer;
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

function SemverSplitPre(const V: String; var Pre: String): String;
var
  D: Integer;
begin
  D := Pos('-', V);
  if D > 0 then begin
    Pre := Copy(V, D + 1, MaxInt);
    Result := Copy(V, 1, D - 1);
  end else begin
    Pre := '';
    Result := V;
  end;
end;

{ semver compare with prerelease precedence; the numeric-only CompareVersion
  above would rank "1.0.21" below "1.0.21-dev.17" }
function CompareRuntimeSemver(A, B: String): Integer;
var
  CoreA, CoreB, PreA, PreB: String;
  NA, NB: Integer;
begin
  CoreA := SemverSplitPre(A, PreA);
  CoreB := SemverSplitPre(B, PreB);
  while True do begin
    NA := SemverNum(CoreA);
    NB := SemverNum(CoreB);
    if NA < NB then begin
      Result := -1;
      Exit;
    end;
    if NA > NB then begin
      Result := 1;
      Exit;
    end;
    if (CoreA = '') and (CoreB = '') then
      Break;
  end;
  if (PreA = '') and (PreB = '') then begin
    Result := 0;
    Exit;
  end;
  if PreA = '' then begin
    Result := 1;
    Exit;
  end;
  if PreB = '' then begin
    Result := -1;
    Exit;
  end;
  while True do begin
    NA := SemverNum(PreA);
    NB := SemverNum(PreB);
    if NA < NB then begin
      Result := -1;
      Exit;
    end;
    if NA > NB then begin
      Result := 1;
      Exit;
    end;
    if (PreA = '') and (PreB = '') then begin
      Result := 0;
      Exit;
    end;
  end;
end;

function ShouldInstallRuntime(): Boolean;
var
  Existing: String;
begin
  Result := True;
  if not FileExists(RuntimeStateFile()) then
    Exit;
  Existing := ExistingRuntimeVersion();
  if Existing = '' then
    Exit;
  if CompareRuntimeSemver('{#RtVersion}', Existing) < 0 then begin
    Log(Format('existing managed runtime %s is newer than bundled %s; keeping it', [Existing, '{#RtVersion}']));
    Result := False;
  end;
end;

procedure WriteRuntimeState;
var
  StateDir, Content: String;
begin
  if not ShouldInstallRuntime then
    Exit;
  StateDir := ExpandConstant('{%USERPROFILE}') + '\.rsrs\client2';
  if not ForceDirectories(StateDir) then begin
    Log('cannot create ' + StateDir);
    Exit;
  end;
  Content := '{"version":"{#RtVersion}","sha256":"{#RtSha256}"}';
  SaveStringToFile(RuntimeStateFile(), Content, False);
  Log('runtime state written: ' + Content);
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
    InstallCli;
    WriteRuntimeState;
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
  UninstallPathAdded := RegQueryDwordValue(HKCU, 'Software\Respire\Installer', 'PathAdded', Marker);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usUninstall then
    RemoveUserPath;
end;

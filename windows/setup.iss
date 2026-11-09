#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceExe
  #define SourceExe "..\dist\mikrotik-inspector.exe"
#endif
#ifndef OutputDir
  #define OutputDir "..\dist"
#endif
[Setup]
AppId={{E7CE0D92-6D29-4B67-9F5D-C8D25A504437}
AppName=MikroTik Inspector
AppVersion={#AppVersion}
AppPublisher=MikroTik Inspector
DefaultDirName={localappdata}\Programs\MikroTik Inspector
DefaultGroupName=MikroTik Inspector
PrivilegesRequired=lowest
OutputDir={#OutputDir}
OutputBaseFilename=mikrotik-inspector-v{#AppVersion}-windows-setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayIcon={app}\MikroTikInspector.exe
[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "MikroTikInspector.exe"; Flags: ignoreversion
[Icons]
Name: "{group}\MikroTik Inspector"; Filename: "{app}\MikroTikInspector.exe"
Name: "{autodesktop}\MikroTik Inspector"; Filename: "{app}\MikroTikInspector.exe"; Tasks: desktopicon
[Tasks]
Name: "desktopicon"; Description: "Создать значок на рабочем столе"; Flags: unchecked
[Run]
Filename: "{app}\MikroTikInspector.exe"; Description: "Запустить MikroTik Inspector"; Flags: nowait postinstall skipifsilent

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceExe
  #define SourceExe "..\dist\mikrotik-inspector.exe"
#endif
#ifndef OutputDir
  #define OutputDir "..\dist"
#endif
; Иконка создаётся из procedural отрисовки командой tools/makeicon,
; поэтому бинарных ресурсов в репозитории нет. По умолчанию ищется рядом
; со сценарием (windows/app.ico); сборка передаёт абсолютный путь.
#ifndef SetupIcon
  #define SetupIcon "app.ico"
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
SetupIconFile={#SetupIcon}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayIcon={app}\app.ico
UninstallDisplayName=MikroTik Inspector
[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "MikroTikInspector.exe"; Flags: ignoreversion
Source: "{#SetupIcon}"; DestDir: "{app}"; DestName: "app.ico"; Flags: ignoreversion
[Icons]
; Без IconFilename ярлык берёт иконку из exe, а у собранного Go-приложения
; своего ресурса иконки нет — поэтому указываем .ico явно.
Name: "{group}\MikroTik Inspector"; Filename: "{app}\MikroTikInspector.exe"; IconFilename: "{app}\app.ico"
Name: "{autodesktop}\MikroTik Inspector"; Filename: "{app}\MikroTikInspector.exe"; IconFilename: "{app}\app.ico"; Tasks: desktopicon
[Tasks]
Name: "desktopicon"; Description: "Создать значок на рабочем столе"; Flags: unchecked
[Run]
Filename: "{app}\MikroTikInspector.exe"; Description: "Запустить MikroTik Inspector"; Flags: nowait postinstall skipifsilent runascurrentuser

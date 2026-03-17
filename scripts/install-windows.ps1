param(
  [string]$BinaryPath = (Join-Path $PSScriptRoot "..\immich-uploader-windows-amd64.exe"),
  [string]$InstallDir = (Join-Path $env:LOCALAPPDATA "Programs\immich-uploader"),
  [switch]$Uninstall
)

$ErrorActionPreference = "Stop"

function Resolve-AbsolutePath {
  param([string]$Path)

  if ([System.IO.Path]::IsPathRooted($Path)) {
    return (Resolve-Path $Path).Path
  }

  return (Resolve-Path (Join-Path $PSScriptRoot $Path)).Path
}

function Add-ContextMenu {
  param([string]$ExePath)

  $folderKey = "HKCU\Software\Classes\Directory\shell\ImmichUploader"
  $folderCmdKey = "$folderKey\command"
  $bgKey = "HKCU\Software\Classes\Directory\Background\shell\ImmichUploader"
  $bgCmdKey = "$bgKey\command"
  $folderCommand = "`"$ExePath`" --root `"%1`" --autostart"
  $backgroundCommand = "`"$ExePath`" --root `"%V`" --autostart"

  reg.exe add $folderKey /ve /d "Upload with Immich Uploader" /f | Out-Null
  reg.exe add $folderKey /v Icon /d $ExePath /f | Out-Null
  reg.exe add $folderCmdKey /ve /d $folderCommand /f | Out-Null

  reg.exe add $bgKey /ve /d "Upload this folder with Immich Uploader" /f | Out-Null
  reg.exe add $bgKey /v Icon /d $ExePath /f | Out-Null
  reg.exe add $bgCmdKey /ve /d $backgroundCommand /f | Out-Null
}

function Remove-ContextMenu {
  $keys = @(
    "HKCU\Software\Classes\Directory\shell\ImmichUploader",
    "HKCU\Software\Classes\Directory\Background\shell\ImmichUploader"
  )

  foreach ($key in $keys) {
    reg.exe delete $key /f 2>$null | Out-Null
  }
}

$binary = Resolve-AbsolutePath $BinaryPath
$install = $InstallDir
$installedExe = Join-Path $install "immich-uploader.exe"

if ($Uninstall) {
  Remove-ContextMenu
  if (Test-Path $install) {
    Remove-Item -Recurse -Force $install
  }
  Write-Host "Removed Immich Uploader from $install"
  exit 0
}

if (-not (Test-Path $binary)) {
  throw "Binary not found: $binary"
}

New-Item -ItemType Directory -Force -Path $install | Out-Null
Copy-Item -Force $binary $installedExe
Add-ContextMenu -ExePath $installedExe

Write-Host "Installed Immich Uploader to $installedExe"
Write-Host "Explorer context menus added for folders and folder backgrounds."
Write-Host "To remove it later, run:"
Write-Host "  powershell -ExecutionPolicy Bypass -File `"$PSScriptRoot\install-windows.ps1`" -Uninstall"

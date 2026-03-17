# immich-uploader (TUI)

Interactive Bubble Tea app to upload media folders into Immich albums.

Behavior:
1) creates (or reuses) an Immich album for each folder under root,
2) uploads media files,
3) adds uploaded assets to the album,
4) either moves local files to `ignore/` or (optionally) verifies checksum then deletes.

## Requirements
- Immich server reachable
- An Immich API key (Settings -> API Keys)
- Go 1.23+

## Build

```bash
go build -o immich-uploader ./cmd/tui
```

## Linux PATH setup

```bash
./scripts/install-linux-path.sh
```

This script:
- ensures `~/.local/bin` exists,
- adds it to shell startup files,
- installs `immich-uploader` and `immich-uploader-tui` from the built TUI binary.

## Windows install

Build the Windows GUI binary:

```powershell
go build -o immich-uploader-windows-amd64.exe ./cmd/gui
```

Then install it and register Explorer folder actions:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install-windows.ps1
```

If you prefer a double-clickable installer, run `scripts\install-windows.cmd` instead.

This script:
- copies the Windows GUI binary into `%LOCALAPPDATA%\Programs\immich-uploader`,
- registers folder and folder-background context-menu entries in Windows Explorer,
- launches the app with the selected folder as `--root` and enables `--autostart`.

Windows shell integration is added as a right-click Explorer menu item, not as a replacement for normal left-click folder behavior.

## Run

```bash
./immich-uploader \
  --immich "https://immich.example.com/api" \
  --key "YOUR_IMMICH_API_KEY" \
  --root "/path/to/photos"
```

## Startup flow
- First run (or `--wizard`) opens setup wizard.
- After config exists, app opens a ready screen (no auto-upload) and calculates a pre-upload plan (albums/files/total size + estimated albums to create).

Ready screen keys:
- `s`: start upload
- `w`: open wizard
- `r`: refresh upload plan
- `q`: quit

Wizard keys:
- `Tab` / `Shift+Tab`: move fields
- `Ctrl+S`: save config and start upload
- `q`: quit

Running keys:
- `q` / `Ctrl+C`: quit
- `v`: toggle event log panel
- `?`: toggle help
- `j`/`k`, arrow keys, `pgup`/`pgdn`, `g`/`G`: scroll logs

## Config file
- Default: `~/.config/immich-uploader/tui-config.json`
- Custom path: `--config /path/to/file.json`

## Key flags
- `--immich`: base API URL including `/api`
- `--key`: Immich API key (`x-api-key`)
- `--root`: root folder containing album folders (use `.` for current folder)
- `--workers`: parallel upload workers per album
- `--batch`: assets per album-add request
- `--checksum`: send SHA1 checksum header on upload
- `--dedupe-add`: pre-check duplicates via Immich and add existing assets to album without re-upload
- `--delete-on-success`: verify uploaded checksum via API, then permanently delete local file
- `--ignore-dir`: folder name to skip and move successful files into (when not deleting)

## Notes
- Uses file `mtime` for both `fileCreatedAt` and `fileModifiedAt`.
- Filters to common photo/video extensions.
- If `--delete-on-success=true`, file is deleted only when local SHA1 matches asset checksum returned by Immich.
- If `--dedupe-add=true`, uploader uses preflight duplicate check and skips uploading files that already exist on server.
- Empty directories are pruned after move/delete.

## API endpoints used
- `GET /albums`
- `POST /albums`
- `POST /assets`
- `POST /assets/bulk-upload-check` (duplicate pre-check)
- `GET /assets/{id}` (checksum verification for delete mode)
- `PUT /albums/{id}/assets`

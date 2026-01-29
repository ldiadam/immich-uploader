# immich-uploader (albums from folders)

Simple Go program that:
1) creates (or reuses) an Immich album for each **folder name** under a root directory, then
2) uploads all media files in that folder, then
3) adds uploaded assets to the album.

## Requirements
- Immich server reachable
- An Immich **API key** (Settings → API Keys)
- Go 1.20+

## Build

```bash
go build -o immich-uploader ./
```

## Run

```bash
./immich-uploader \
  --immich "https://immich.example.com/api" \
  --key "YOUR_IMMICH_API_KEY" \
  --root "/path/to/photos" \
  --deep=true
```

### Flags
- `--immich`: base API URL **including `/api`** (e.g. `http://localhost:2283/api`)
- `--key`: Immich API key (sent as header `x-api-key`)
- `--root`: root folder containing album folders
- `--deep`: if true (default), uploads nested subfolders too
- `--checksum`: if true (default), computes sha1 of each file and sends `x-immich-checksum` (slower but better duplicate detection)
- `--batch`: how many uploaded assets to add per album request

## Notes
- Uses file `mtime` for both `fileCreatedAt` and `fileModifiedAt`.
- Filters to common photo/video extensions.
- If an album with the same name already exists, it reuses it.
- An `ignore/<AlbumName>/` folder is created as soon as the album is processed.
- Each file is moved into `ignore/<AlbumName>/...` immediately after its upload succeeds (preserving subfolder structure).

## API endpoints used
- `GET /albums`
- `POST /albums`
- `POST /assets` (multipart upload)
- `PUT /albums/{id}/assets`

- `--ignore-dir`: folder name to skip at root and to move successfully uploaded folders into (default `ignore`).

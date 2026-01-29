package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Immich API (v2 stable):
// - POST   /albums                 (CreateAlbumDto)
// - GET    /albums                 (AlbumResponseDto[])
// - PUT    /albums/{id}/assets     (BulkIdsDto)
// - POST   /assets                 (multipart AssetMediaCreateDto)
// Auth: x-api-key: <api key>

type albumResponse struct {
	ID        string `json:"id"`
	AlbumName string `json:"albumName"`
}

type createAlbumRequest struct {
	AlbumName string `json:"albumName"`
}

type assetUploadResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type bulkIDs struct {
	IDs []string `json:"ids"`
}

type client struct {
	baseURL string
	apiKey  string
	hc      *http.Client
}

func (c *client) doJSON(ctx context.Context, method, urlPath string, reqBody any, out any) error {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+urlPath, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed: status=%d body=%s", method, urlPath, resp.StatusCode, strings.TrimSpace(string(b)))
	}

	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("decode response: %w (body=%s)", err, strings.TrimSpace(string(b)))
		}
	}
	return nil
}

func (c *client) getAllAlbums(ctx context.Context) (map[string]string, error) {
	var albums []albumResponse
	if err := c.doJSON(ctx, http.MethodGet, "/albums", nil, &albums); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(albums))
	for _, a := range albums {
		m[a.AlbumName] = a.ID
	}
	return m, nil
}

func (c *client) createAlbum(ctx context.Context, name string) (string, error) {
	var out albumResponse
	if err := c.doJSON(ctx, http.MethodPost, "/albums", createAlbumRequest{AlbumName: name}, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *client) addAssetsToAlbum(ctx context.Context, albumID string, assetIDs []string) error {
	if len(assetIDs) == 0 {
		return nil
	}
	path := fmt.Sprintf("/albums/%s/assets", albumID)
	return c.doJSON(ctx, http.MethodPut, path, bulkIDs{IDs: assetIDs}, nil)
}

func (c *client) uploadAsset(ctx context.Context, filePath, deviceID, deviceAssetID string, createdAt, modifiedAt time.Time, checksumSHA1 string) (assetUploadResponse, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return assetUploadResponse{}, err
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// required fields
	_ = mw.WriteField("deviceId", deviceID)
	_ = mw.WriteField("deviceAssetId", deviceAssetID)
	_ = mw.WriteField("fileCreatedAt", createdAt.UTC().Format(time.RFC3339Nano))
	_ = mw.WriteField("fileModifiedAt", modifiedAt.UTC().Format(time.RFC3339Nano))
	_ = mw.WriteField("filename", filepath.Base(filePath))

	part, err := mw.CreateFormFile("assetData", filepath.Base(filePath))
	if err != nil {
		return assetUploadResponse{}, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return assetUploadResponse{}, err
	}

	if err := mw.Close(); err != nil {
		return assetUploadResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/assets", &buf)
	if err != nil {
		return assetUploadResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if checksumSHA1 != "" {
		req.Header.Set("x-immich-checksum", checksumSHA1)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return assetUploadResponse{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return assetUploadResponse{}, fmt.Errorf("upload failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out assetUploadResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return assetUploadResponse{}, fmt.Errorf("decode upload response: %w (body=%s)", err, strings.TrimSpace(string(b)))
	}
	return out, nil
}

func sha1HexString(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func sha1File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isMediaFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".tif", ".tiff", ".bmp",
		".mp4", ".mov", ".m4v", ".mkv", ".avi", ".webm":
		return true
	default:
		return false
	}
}

func chunk[T any](in []T, n int) [][]T {
	if n <= 0 {
		return [][]T{in}
	}
	var out [][]T
	for i := 0; i < len(in); i += n {
		j := i + n
		if j > len(in) {
			j = len(in)
		}
		out = append(out, in[i:j])
	}
	return out
}

func main() {
	var (
		baseURL   = flag.String("immich", "http://localhost:2283/api", "Immich base API URL (include /api). Example: https://photos.example.com/api")
		apiKey    = flag.String("key", "", "Immich API key (x-api-key)")
		root      = flag.String("root", "", "Root folder containing album folders")
		deep      = flag.Bool("deep", true, "If true (default), upload files from nested subfolders under each album folder")
		checksum  = flag.Bool("checksum", false, "If true, compute sha1 checksum and send x-immich-checksum header (slower)")
		batchSize = flag.Int("batch", 200, "How many uploaded assets to add to album per request")
		timeout   = flag.Duration("timeout", 5*time.Minute, "HTTP timeout")
	)
	flag.Parse()

	if *apiKey == "" {
		fmt.Fprintln(os.Stderr, "missing --key")
		os.Exit(2)
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "missing --root")
		os.Exit(2)
	}

	// normalize base url
	b := strings.TrimRight(*baseURL, "/")

	c := &client{baseURL: b, apiKey: *apiKey, hc: &http.Client{Timeout: *timeout}}
	ctx := context.Background()

	albums, err := c.getAllAlbums(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to list albums: %v\n", err)
		os.Exit(1)
	}

	entries, err := os.ReadDir(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read root dir: %v\n", err)
		os.Exit(1)
	}

	deviceID := "immich-folder-uploader-" + runtime.GOOS

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		folderName := e.Name()
		folderPath := filepath.Join(*root, folderName)

		albumID, ok := albums[folderName]
		if !ok {
			fmt.Printf("Creating album: %s\n", folderName)
			id, err := c.createAlbum(ctx, folderName)
			if err != nil {
				fmt.Fprintf(os.Stderr, "create album %q failed: %v\n", folderName, err)
				continue
			}
			albumID = id
			albums[folderName] = id
		} else {
			fmt.Printf("Using existing album: %s\n", folderName)
		}

		var files []string
		walkFn := func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != folderPath && !*deep {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") {
				return nil
			}
			if !isMediaFile(name) {
				return nil
			}
			files = append(files, path)
			return nil
		}

		if err := filepath.WalkDir(folderPath, walkFn); err != nil {
			fmt.Fprintf(os.Stderr, "walk %s: %v\n", folderName, err)
			continue
		}
		if len(files) == 0 {
			fmt.Printf("No media files in %s, skipping\n", folderName)
			continue
		}

		fmt.Printf("Uploading %d files from %s...\n", len(files), folderName)
		var uploadedIDs []string
		for i, fp := range files {
			st, err := os.Stat(fp)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stat %s: %v\n", fp, err)
				continue
			}

			// deviceAssetId should be stable per file
			rel, _ := filepath.Rel(*root, fp)
			deviceAssetID := sha1HexString(rel)

			created := st.ModTime()
			modified := st.ModTime()

			var sum string
			if *checksum {
				s, err := sha1File(fp)
				if err != nil {
					fmt.Fprintf(os.Stderr, "checksum %s: %v\n", fp, err)
				} else {
					sum = s
				}
			}

			resp, err := c.uploadAsset(ctx, fp, deviceID, deviceAssetID, created, modified, sum)
			if err != nil {
				fmt.Fprintf(os.Stderr, "upload failed (%s): %v\n", fp, err)
				continue
			}
			uploadedIDs = append(uploadedIDs, resp.ID)
			fmt.Printf("  [%d/%d] %s -> %s (%s)\n", i+1, len(files), filepath.Base(fp), resp.ID, resp.Status)
		}

		if len(uploadedIDs) == 0 {
			fmt.Printf("No uploads succeeded for %s\n", folderName)
			continue
		}

		// Add to album in batches
		for _, ch := range chunk(uploadedIDs, *batchSize) {
			if err := c.addAssetsToAlbum(ctx, albumID, ch); err != nil {
				fmt.Fprintf(os.Stderr, "add assets to album %s failed: %v\n", folderName, err)
				// keep going
			}
		}
		fmt.Printf("Album %s: added %d assets\n", folderName, len(uploadedIDs))
	}
}

// NOTE: This is a simple uploader.
// - It uses file modtime for both fileCreatedAt/fileModifiedAt.
// - It skips non-media extensions.
// - For very large libraries, add concurrency + retry/backoff.

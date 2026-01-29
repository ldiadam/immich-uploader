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
	"sort"
	"strings"
	"sync"
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

func moveToIgnore(root, ignoreName, folderName string) error {
	ignorePath := filepath.Join(root, ignoreName)
	if err := os.MkdirAll(ignorePath, 0o755); err != nil {
		return err
	}
	src := filepath.Join(root, folderName)
	dst := filepath.Join(ignorePath, folderName)
	if _, err := os.Stat(dst); err == nil {
		// collision: append timestamp
		dst = filepath.Join(ignorePath, fmt.Sprintf("%s-%d", folderName, time.Now().Unix()))
	}
	return os.Rename(src, dst)
}

func ensureIgnoreAlbumDir(root, ignoreName, albumName string) (string, error) {
	base := filepath.Join(root, ignoreName, albumName)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	return base, nil
}

func moveFileToIgnore(root, ignoreName, albumName, albumRoot, srcPath string) error {
	// preserve relative path under the album root (including subfolders)
	rel, err := filepath.Rel(albumRoot, srcPath)
	if err != nil {
		rel = filepath.Base(srcPath)
	}
	dst := filepath.Join(root, ignoreName, albumName, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// collision handling
	if _, err := os.Stat(dst); err == nil {
		ext := filepath.Ext(dst)
		base := strings.TrimSuffix(filepath.Base(dst), ext)
		dst = filepath.Join(filepath.Dir(dst), fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext))
	}
	return os.Rename(srcPath, dst)
}

func formatBytes(n int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.2fGiB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.2fMiB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.2fKiB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func formatRate(bytes int64, d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	rate := float64(bytes) / d.Seconds() // B/s
	return fmt.Sprintf("%s/s", formatBytes(int64(rate)))
}

func main() {
	var (
		baseURL       = flag.String("immich", "http://localhost:2283/api", "Immich base API URL (include /api). Example: https://photos.example.com/api")
		apiKey        = flag.String("key", "", "Immich API key (x-api-key)")
		root          = flag.String("root", "", "Root folder containing album folders")
		deep          = flag.Bool("deep", true, "If true (default), upload files from nested subfolders under each album folder")
		checksum      = flag.Bool("checksum", true, "If true, compute sha1 checksum and send x-immich-checksum header (slower)")
		batchSize     = flag.Int("batch", 200, "How many uploaded assets to add to album per request")
		workers       = flag.Int("workers", 4, "Number of parallel upload workers per album")
		smallestFirst = flag.Bool("smallest-first", true, "Upload smaller files first")
		timeout       = flag.Duration("timeout", 5*time.Minute, "HTTP timeout")
		ignoreDir     = flag.String("ignore-dir", "ignore", "Folder name to ignore (and destination for moved folders)")
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
		if folderName == *ignoreDir {
			continue
		}
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

		if _, err := ensureIgnoreAlbumDir(*root, *ignoreDir, folderName); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create ignore folder for %s: %v\n", folderName, err)
			continue
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

		if *smallestFirst {
			sort.Slice(files, func(i, j int) bool {
				sti, err1 := os.Stat(files[i])
				stj, err2 := os.Stat(files[j])
				if err1 != nil && err2 != nil {
					return files[i] < files[j]
				}
				if err1 != nil {
					return false
				}
				if err2 != nil {
					return true
				}
				if sti.Size() == stj.Size() {
					return files[i] < files[j]
				}
				return sti.Size() < stj.Size()
			})
		}

		totalBytes := int64(0)
		for _, fp := range files {
			if st, err := os.Stat(fp); err == nil {
				totalBytes += st.Size()
			}
		}
		albumStart := time.Now()
		uploadedBytes := int64(0)
		fmt.Printf("Uploading %d files (%s) from %s...\n", len(files), formatBytes(totalBytes), folderName)
		var uploadedIDs []string
		uploadErrors := 0

		type uploadJob struct {
			idx  int
			path string
			size int64
		}
		type uploadResult struct {
			idx   int
			path  string
			size  int64
			asset assetUploadResponse
			dur   time.Duration
			err   error
		}

		jobs := make(chan uploadJob)
		results := make(chan uploadResult)
		wg := sync.WaitGroup{}

		workerCount := *workers
		if workerCount < 1 {
			workerCount = 1
		}
		if workerCount > len(files) {
			workerCount = len(files)
		}

		for w := 0; w < workerCount; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobs {
					st, err := os.Stat(job.path)
					if err != nil {
						results <- uploadResult{idx: job.idx, path: job.path, size: job.size, err: err}
						continue
					}

					rel, _ := filepath.Rel(*root, job.path)
					deviceAssetID := sha1HexString(rel)
					created := st.ModTime()
					modified := st.ModTime()

					var sum string
					if *checksum {
						s, err := sha1File(job.path)
						if err == nil {
							sum = s
						}
					}

					fileStart := time.Now()
					asset, err := c.uploadAsset(ctx, job.path, deviceID, deviceAssetID, created, modified, sum)
					fileDur := time.Since(fileStart)
					if err == nil {
						// move source file immediately after successful upload
						if merr := moveFileToIgnore(*root, *ignoreDir, folderName, folderPath, job.path); merr != nil {
							fmt.Fprintf(os.Stderr, "move failed (%s): %v\n", job.path, merr)
						}
					}
					results <- uploadResult{idx: job.idx, path: job.path, size: job.size, asset: asset, dur: fileDur, err: err}
				}
			}()
		}

		go func() {
			for i, fp := range files {
				st, err := os.Stat(fp)
				if err != nil {
					// still send as error result
					results <- uploadResult{idx: i, path: fp, size: 0, err: err}
					continue
				}
				jobs <- uploadJob{idx: i, path: fp, size: st.Size()}
			}
			close(jobs)
			wg.Wait()
			close(results)
		}()

		// collect results
		completed := 0
		uploadedBytesMu := sync.Mutex{}
		for res := range results {
			completed++
			if res.err != nil {
				uploadErrors++
				fmt.Fprintf(os.Stderr, "upload failed (%s): %v\n", res.path, res.err)
				continue
			}
			uploadedIDs = append(uploadedIDs, res.asset.ID)
			uploadedBytesMu.Lock()
			uploadedBytes += res.size
			elapsed := time.Since(albumStart)
			fmt.Printf("    Progress: %d/%d (%s/%s) | avg %s | last %s (%s)\n",
				completed, len(files), formatBytes(uploadedBytes), formatBytes(totalBytes), formatRate(uploadedBytes, elapsed), formatRate(res.size, res.dur), res.dur.Round(time.Millisecond))
			fmt.Printf("  [%d/%d] %s -> %s (%s)\n", completed, len(files), filepath.Base(res.path), res.asset.ID, res.asset.Status)
			uploadedBytesMu.Unlock()
		}

		if len(uploadedIDs) == 0 {
			fmt.Printf("No uploads succeeded for %s\n", folderName)
			continue
		}

		// Add to album in batches
		for _, ch := range chunk(uploadedIDs, *batchSize) {
			if err := c.addAssetsToAlbum(ctx, albumID, ch); err != nil {
				fmt.Fprintf(os.Stderr, "add assets to album %s failed: %v\n", folderName, err)
			}
		}
		fmt.Printf("Album %s: added %d assets\n", folderName, len(uploadedIDs))

	}
}

// NOTE: This is a simple uploader.
// - It uses file modtime for both fileCreatedAt/fileModifiedAt.
// - It skips non-media extensions.
// - For very large libraries, add concurrency + retry/backoff.

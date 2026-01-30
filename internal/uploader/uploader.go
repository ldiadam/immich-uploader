package uploader

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"syscall"
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
	// Stream multipart upload using io.Pipe to avoid buffering entire files in RAM.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()

	go func() {
		defer func() {
			_ = pw.Close()
		}()

		// required fields
		if err := mw.WriteField("deviceId", deviceID); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("deviceAssetId", deviceAssetID); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("fileCreatedAt", createdAt.UTC().Format(time.RFC3339Nano)); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("fileModifiedAt", modifiedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("filename", filepath.Base(filePath)); err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		part, err := mw.CreateFormFile("assetData", filepath.Base(filePath))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		f, err := os.Open(filePath)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_, err = io.Copy(part, f)
		_ = f.Close()
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if err := mw.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/assets", pr)
	if err != nil {
		return assetUploadResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Content-Type", contentType)
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

	// On Windows, renames can fail transiently with sharing violations (e.g. AV scan / Explorer preview).
	// Retry a few times before giving up.
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		err := os.Rename(srcPath, dst)
		if err == nil {
			return nil
		}
		lastErr = err

		// Retry only for common "file in use" cases.
		var errno syscall.Errno
		if errors.As(err, &errno) {
			if errno != 32 && errno != 33 { // ERROR_SHARING_VIOLATION / ERROR_LOCK_VIOLATION
				break
			}
		} else if !strings.Contains(strings.ToLower(err.Error()), "being used by another process") {
			break
		}

		time.Sleep(time.Duration(150*(attempt+1)) * time.Millisecond)
	}
	return lastErr
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

type Options struct {
	BaseURL       string
	APIKey        string
	Root          string
	Deep          bool
	Checksum      bool
	BatchSize     int
	Workers       int
	SmallestFirst bool
	IgnoreDir     string
	Timeout       time.Duration
	// DedupeAdd: if true, rely on server-side checksum dedupe during upload.
	// (Future: add /assets/bulk-upload-check preflight.)
	DedupeAdd bool
}

type Logf func(format string, args ...any)

func Run(ctx context.Context, opt Options, logf Logf) error {
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Printf(format, args...) }
	}
	if opt.APIKey == "" {
		return fmt.Errorf("missing API key")
	}
	if opt.Root == "" {
		return fmt.Errorf("missing root")
	}

	b := strings.TrimRight(opt.BaseURL, "/")
	c := &client{baseURL: b, apiKey: opt.APIKey, hc: &http.Client{Timeout: opt.Timeout}}

	albums, err := c.getAllAlbums(ctx)
	if err != nil {
		return fmt.Errorf("failed to list albums: %w", err)
	}

	entries, err := os.ReadDir(opt.Root)
	if err != nil {
		return fmt.Errorf("read root dir: %w", err)
	}

	deviceID := "immich-folder-uploader-" + runtime.GOOS

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		folderName := e.Name()
		if folderName == opt.IgnoreDir {
			continue
		}
		folderPath := filepath.Join(opt.Root, folderName)

		albumID, ok := albums[folderName]
		if !ok {
			logf("Creating album: %s\n", folderName)
			id, err := c.createAlbum(ctx, folderName)
			if err != nil {
				logf("create album %q failed: %v\n", folderName, err)
				continue
			}
			albumID = id
			albums[folderName] = id
		} else {
			logf("Using existing album: %s\n", folderName)
		}

		if _, err := ensureIgnoreAlbumDir(opt.Root, opt.IgnoreDir, folderName); err != nil {
			logf("failed to create ignore folder for %s: %v\n", folderName, err)
			continue
		}

		var files []string
		walkFn := func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != folderPath && !opt.Deep {
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
			logf("walk %s: %v\n", folderName, err)
			continue
		}
		if len(files) == 0 {
			logf("No media files in %s, skipping\n", folderName)
			continue
		}

		if opt.SmallestFirst {
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
		logf("Uploading %d files (%s) from %s...\n", len(files), formatBytes(totalBytes), folderName)

		uploadedIDs := make([]string, 0, len(files))
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

		workerCount := opt.Workers
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

					rel, _ := filepath.Rel(opt.Root, job.path)
					deviceAssetID := sha1HexString(rel)
					created := st.ModTime()
					modified := st.ModTime()

					sum := ""
					if opt.Checksum {
						s, err := sha1File(job.path)
						if err == nil {
							sum = s
						}
					}

					fileStart := time.Now()
					asset, err := c.uploadAsset(ctx, job.path, deviceID, deviceAssetID, created, modified, sum)
					fileDur := time.Since(fileStart)
					if err == nil {
						if merr := moveFileToIgnore(opt.Root, opt.IgnoreDir, folderName, folderPath, job.path); merr != nil {
							logf("move failed (%s): %v\n", job.path, merr)
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
					results <- uploadResult{idx: i, path: fp, size: 0, err: err}
					continue
				}
				jobs <- uploadJob{idx: i, path: fp, size: st.Size()}
			}
			close(jobs)
			wg.Wait()
			close(results)
		}()

		completed := 0
		uploadedBytesMu := sync.Mutex{}
		for res := range results {
			completed++
			if res.err != nil {
				uploadErrors++
				logf("upload failed (%s): %v\n", res.path, res.err)
				continue
			}
			uploadedIDs = append(uploadedIDs, res.asset.ID)
			uploadedBytesMu.Lock()
			uploadedBytes += res.size
			elapsed := time.Since(albumStart)
			logf("    Progress: %d/%d (%s/%s) | avg %s | last %s (%s)\n",
				completed, len(files), formatBytes(uploadedBytes), formatBytes(totalBytes), formatRate(uploadedBytes, elapsed), formatRate(res.size, res.dur), res.dur.Round(time.Millisecond))
			logf("  [%d/%d] %s -> %s (%s)\n", completed, len(files), filepath.Base(res.path), res.asset.ID, res.asset.Status)
			uploadedBytesMu.Unlock()
		}

		if len(uploadedIDs) == 0 {
			logf("No uploads succeeded for %s\n", folderName)
			continue
		}

		if uploadErrors > 0 {
			logf("Album %s: %d upload errors (still adding successful assets to album)\n", folderName, uploadErrors)
		}

		for _, ch := range chunk(uploadedIDs, opt.BatchSize) {
			if err := c.addAssetsToAlbum(ctx, albumID, ch); err != nil {
				logf("add assets to album %s failed: %v\n", folderName, err)
			}
		}
		logf("Album %s: added %d assets\n", folderName, len(uploadedIDs))
	}

	return nil
}

// NOTE: This is a simple uploader.
// - It uses file modtime for both fileCreatedAt/fileModifiedAt.
// - It skips non-media extensions.
// - For very large libraries, add concurrency + retry/backoff.

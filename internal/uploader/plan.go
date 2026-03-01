package uploader

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type UploadPlan struct {
	AlbumsTotal    int
	AlbumsExisting int
	AlbumsToCreate int
	ServerChecked  bool
	MediaFiles     int
	TotalBytes     int64
}

func BuildUploadPlan(ctx context.Context, opt Options) (UploadPlan, error) {
	var plan UploadPlan

	if strings.TrimSpace(opt.Root) == "" {
		return plan, fmt.Errorf("missing root")
	}
	st, err := os.Stat(opt.Root)
	if err != nil {
		return plan, fmt.Errorf("stat root: %w", err)
	}
	if !st.IsDir() {
		return plan, fmt.Errorf("root is not a directory")
	}

	entries, err := os.ReadDir(opt.Root)
	if err != nil {
		return plan, fmt.Errorf("read root dir: %w", err)
	}

	albumFolders := make([]string, 0, len(entries))
	for _, e := range entries {
		select {
		case <-ctx.Done():
			return plan, ctx.Err()
		default:
		}
		if !e.IsDir() {
			continue
		}
		if e.Name() == opt.IgnoreDir {
			continue
		}
		albumFolders = append(albumFolders, e.Name())
	}
	plan.AlbumsTotal = len(albumFolders)

	if strings.TrimSpace(opt.BaseURL) != "" && strings.TrimSpace(opt.APIKey) != "" {
		timeout := opt.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		c := &client{
			baseURL: strings.TrimRight(opt.BaseURL, "/"),
			apiKey:  opt.APIKey,
			hc:      &http.Client{Timeout: timeout},
		}
		remoteAlbums, err := c.getAllAlbums(ctx)
		if err != nil {
			return plan, fmt.Errorf("list remote albums: %w", err)
		}
		for _, name := range albumFolders {
			if _, ok := remoteAlbums[name]; ok {
				plan.AlbumsExisting++
			}
		}
		plan.AlbumsToCreate = plan.AlbumsTotal - plan.AlbumsExisting
		plan.ServerChecked = true
	}

	for _, folderName := range albumFolders {
		select {
		case <-ctx.Done():
			return plan, ctx.Err()
		default:
		}

		folderPath := filepath.Join(opt.Root, folderName)
		walkFn := func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
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

			plan.MediaFiles++
			if info, err := d.Info(); err == nil {
				plan.TotalBytes += info.Size()
			}
			return nil
		}

		if err := filepath.WalkDir(folderPath, walkFn); err != nil {
			return plan, fmt.Errorf("walk %s: %w", folderName, err)
		}
	}

	return plan, nil
}

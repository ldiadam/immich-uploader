package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"immich-uploader/internal/uploader"
)

func main() {
	var (
		baseURL       = flag.String("immich", "http://localhost:2283/api", "Immich base API URL (include /api). Example: https://photos.example.com/api")
		apiKey        = flag.String("key", "", "Immich API key (x-api-key)")
		root          = flag.String("root", "", "Root folder containing album folders")
		deep          = flag.Bool("deep", true, "If true (default), upload files from nested subfolders under each album folder")
		checksum      = flag.Bool("checksum", true, "If true (default), compute sha1 checksum and send x-immich-checksum header")
		batchSize     = flag.Int("batch", 200, "How many uploaded assets to add to album per request")
		workers       = flag.Int("workers", 4, "Number of parallel upload workers per album")
		smallestFirst = flag.Bool("smallest-first", true, "Upload smaller files first")
		dedupeAdd     = flag.Bool("dedupe-add", true, "If true, rely on checksum dedupe so existing assets can still be added to the album")
		timeout       = flag.Duration("timeout", 5*time.Minute, "HTTP timeout")
		ignoreDir     = flag.String("ignore-dir", "ignore", "Folder name to ignore (and destination for moved folders)")
	)
	flag.Parse()

	opt := uploader.Options{
		BaseURL:       *baseURL,
		APIKey:        *apiKey,
		Root:          *root,
		Deep:          *deep,
		Checksum:      *checksum,
		BatchSize:     *batchSize,
		Workers:       *workers,
		SmallestFirst: *smallestFirst,
		IgnoreDir:     *ignoreDir,
		Timeout:       *timeout,
		DedupeAdd:     *dedupeAdd,
	}

	if err := uploader.Run(context.Background(), opt, func(format string, args ...any) {
		fmt.Printf(format, args...)
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

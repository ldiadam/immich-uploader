//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"immich-uploader/internal/uploader"
)

type Config struct {
	BaseURL       string        `json:"baseUrl"`
	APIKey        string        `json:"apiKey"`
	Root          string        `json:"root"`
	Deep          bool          `json:"deep"`
	Checksum      bool          `json:"checksum"`
	BatchSize     int           `json:"batchSize"`
	Workers       int           `json:"workers"`
	SmallestFirst bool          `json:"smallestFirst"`
	DedupeAdd     bool          `json:"dedupeAdd"`
	IgnoreDir     string        `json:"ignoreDir"`
	Timeout       time.Duration `json:"timeout"`
}

func defaultConfig() Config {
	return Config{
		BaseURL:       "http://localhost:2283/api",
		APIKey:        "",
		Root:          "",
		Deep:          true,
		Checksum:      true,
		BatchSize:     200,
		Workers:       4,
		SmallestFirst: true,
		DedupeAdd:     true,
		IgnoreDir:     "ignore",
		Timeout:       5 * time.Minute,
	}
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "immich-uploader.json"
	}
	return filepath.Join(dir, "immich-uploader", "config.json")
}

func loadConfig() Config {
	cfg := defaultConfig()
	b, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(b, &cfg)
	return cfg
}

func saveConfig(cfg Config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

func main() {
	a := app.New()
	w := a.NewWindow("Immich Uploader")
	w.Resize(fyne.NewSize(860, 620))

	cfg := loadConfig()

	baseURLEntry := widget.NewEntry()
	baseURLEntry.SetText(cfg.BaseURL)
	apiKeyEntry := widget.NewPasswordEntry()
	apiKeyEntry.SetText(cfg.APIKey)
	rootEntry := widget.NewEntry()
	rootEntry.SetText(cfg.Root)

	deepCheck := widget.NewCheck("Deep (include subfolders)", nil)
	deepCheck.SetChecked(cfg.Deep)
	checksumCheck := widget.NewCheck("Checksum (recommended)", nil)
	checksumCheck.SetChecked(cfg.Checksum)
	smallestFirstCheck := widget.NewCheck("Upload smallest files first", nil)
	smallestFirstCheck.SetChecked(cfg.SmallestFirst)
	dedupeAddCheck := widget.NewCheck("If duplicate, add existing asset to album", nil)
	dedupeAddCheck.SetChecked(cfg.DedupeAdd)

	workersEntry := widget.NewEntry()
	workersEntry.SetText(fmt.Sprintf("%d", cfg.Workers))
	batchEntry := widget.NewEntry()
	batchEntry.SetText(fmt.Sprintf("%d", cfg.BatchSize))
	timeoutEntry := widget.NewEntry()
	timeoutEntry.SetText(cfg.Timeout.String())
	ignoreEntry := widget.NewEntry()
	ignoreEntry.SetText(cfg.IgnoreDir)

	logBox := widget.NewMultiLineEntry()
	logBox.Wrapping = fyne.TextWrapBreak
	logBox.Disable()

	scroll := container.NewVScroll(logBox)

	appendLog := func(s string) {
		logBox.Enable()
		logBox.SetText(logBox.Text + s)
		logBox.Disable()
		scroll.ScrollToBottom()
	}

	pickBtn := widget.NewButton("Choose root folder...", func() {
		d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if uri == nil {
				return
			}
			rootEntry.SetText(uri.Path())
		}, w)
		d.Show()
	})

	runningMu := sync.Mutex{}
	running := false

	var startBtn *widget.Button
	startBtn = widget.NewButton("Start upload", func() {
		runningMu.Lock()
		if running {
			runningMu.Unlock()
			return
		}
		running = true
		runningMu.Unlock()

		// collect config
		cfg.BaseURL = baseURLEntry.Text
		cfg.APIKey = apiKeyEntry.Text
		cfg.Root = rootEntry.Text
		cfg.Deep = deepCheck.Checked
		cfg.Checksum = checksumCheck.Checked
		cfg.SmallestFirst = smallestFirstCheck.Checked
		cfg.DedupeAdd = dedupeAddCheck.Checked
		cfg.IgnoreDir = ignoreEntry.Text

		fmt.Sscanf(workersEntry.Text, "%d", &cfg.Workers)
		fmt.Sscanf(batchEntry.Text, "%d", &cfg.BatchSize)
		if d, err := time.ParseDuration(timeoutEntry.Text); err == nil {
			cfg.Timeout = d
		}

		_ = saveConfig(cfg)

		logBox.Enable()
		logBox.SetText("")
		logBox.Disable()

		startBtn.Disable()

		go func() {
			defer func() {
				startBtn.Enable()
				runningMu.Lock()
				running = false
				runningMu.Unlock()
			}()

			opt := uploader.Options{
				BaseURL:       cfg.BaseURL,
				APIKey:        cfg.APIKey,
				Root:          cfg.Root,
				Deep:          cfg.Deep,
				Checksum:      cfg.Checksum,
				BatchSize:     cfg.BatchSize,
				Workers:       cfg.Workers,
				SmallestFirst: cfg.SmallestFirst,
				IgnoreDir:     cfg.IgnoreDir,
				Timeout:       cfg.Timeout,
				DedupeAdd:     cfg.DedupeAdd,
			}

			err := uploader.Run(context.Background(), opt, func(format string, args ...any) {
				msg := fmt.Sprintf(format, args...)
				fyne.Do(func() { appendLog(msg) })
			})

			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, w)
				} else {
					dialog.ShowInformation("Done", "Upload finished", w)
				}
			})
		}()
	})

	form := widget.NewForm(
		widget.NewFormItem("Immich URL", baseURLEntry),
		widget.NewFormItem("API Key", apiKeyEntry),
		widget.NewFormItem("Root Folder", container.NewBorder(nil, nil, nil, pickBtn, rootEntry)),
		widget.NewFormItem("Workers", workersEntry),
		widget.NewFormItem("Batch", batchEntry),
		widget.NewFormItem("Timeout", timeoutEntry),
		widget.NewFormItem("Ignore Dir", ignoreEntry),
	)

	checks := container.NewVBox(deepCheck, checksumCheck, smallestFirstCheck, dedupeAddCheck)

	w.SetContent(container.NewBorder(
		container.NewVBox(form, checks, startBtn),
		nil, nil, nil,
		scroll,
	))

	w.ShowAndRun()
}

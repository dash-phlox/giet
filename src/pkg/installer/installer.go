package installer

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"debug/elf"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"giet/pkg/db"
	"giet/pkg/detect"
	"giet/pkg/github"
	"giet/pkg/utils"

	"github.com/ulikunitz/xz"
)

var QuietMode = false
var VerboseMode = false
var AutoYes = false

func SetQuiet(q bool) {
	QuietMode = q
}

func SetVerbose(v bool) {
	VerboseMode = v
}

func debugPrintf(format string, args ...interface{}) {
	if VerboseMode {
		fmt.Fprintf(os.Stderr, "[LOG] "+format+"\n", args...)
	}
}

func getHomeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

func getBinDir() string {
	return filepath.Join(getHomeDir(), ".local", "bin")
}

func getShareDir() string {
	return filepath.Join(getHomeDir(), ".local", "share")
}

func getApplicationsDir() string {
	return filepath.Join(getShareDir(), "applications")
}

func getIconsDir() string {
	return filepath.Join(getShareDir(), "icons")
}

type progressReader struct {
	reader     io.Reader
	total      int64
	current    int64
	lastTime   time.Time
	lastBytes  int64
	onProgress func(current, total int64, speed float64)
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 {
		pr.current += int64(n)
		now := time.Now()
		if now.Sub(pr.lastTime) >= 200*time.Millisecond {
			elapsed := now.Sub(pr.lastTime).Seconds()
			if elapsed > 0 {
				speed := float64(pr.current-pr.lastBytes) / elapsed / 1024 / 1024
				if pr.onProgress != nil {
					pr.onProgress(pr.current, pr.total, speed)
				}
			}
			pr.lastTime = now
			pr.lastBytes = pr.current
		}
	}
	return n, err
}

func downloadWithProgress(url string, destFile *os.File) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	total := resp.ContentLength
	pr := &progressReader{
		reader:   resp.Body,
		total:    total,
		lastTime: time.Now(),
		onProgress: func(current, total int64, speed float64) {
			if QuietMode {
				return
			}
			if total > 0 {
				percent := float64(current) / float64(total) * 100
				mbDownloaded := float64(current) / 1024 / 1024
				mbTotal := float64(total) / 1024 / 1024
				fmt.Printf("\rDownloading: %.1f%% (%.2f / %.2f MB) at %.2f MB/s", percent, mbDownloaded, mbTotal, speed)
			} else {
				mbDownloaded := float64(current) / 1024 / 1024
				fmt.Printf("\rDownloading: %.2f MB ...", mbDownloaded)
			}
		},
	}

	_, err = io.Copy(destFile, pr)
	if err != nil {
		return err
	}
	if !QuietMode {
		fmt.Println()
	}
	return nil
}

func isValidLinuxAsset(name string) bool {
	lower := strings.ToLower(name)
	forbiddenOS := []string{
		"darwin", "mac", "macos", "windows", "win32", "win64", "win",
		"freebsd", "openbsd", "netbsd", "dragonfly", "dragonflybsd",
		"haiku", "omnios", "solaris", "illumos",
	}
	for _, os := range forbiddenOS {
		if strings.Contains(lower, os) {
			debugPrintf("Asset %s skipped: other OS", name)
			return false
		}
	}
	if detect.HasGLIBC() {
		if strings.Contains(lower, "musl") {
			debugPrintf("Asset %s skipped: musl binary but system has glibc", name)
			return false
		}
	} else {
		if strings.Contains(lower, "gnu") || strings.Contains(lower, "glibc") {
			debugPrintf("Asset %s skipped: glibc binary but system uses musl", name)
			return false
		} else if strings.HasSuffix(lower, ".appimage") {
			debugPrintf("Asset %s skipped: AppImage not supported on musl", name)
			return false
		}
	}
	nonExecPatterns := []string{
		"symbols", "debug", "pdb", "policy", "template", "templates",
		"manifest", "signature", "hash", "checksum", "metadata",
		"doc", "docs", "examples", "demo", "sample",
		"unpacked", "source", "src", "sources",
	}
	for _, p := range nonExecPatterns {
		if strings.Contains(lower, p) {
			debugPrintf("Asset %s skipped: non-executable pattern", name)
			return false
		}
	}
	return true
}

type AssetInfo struct {
	URL  string
	Type string
}

func FindAsset(release *github.GitHubRelease, arch string) (string, []AssetInfo) {
	var candidates []AssetInfo
	var genericCandidates []AssetInfo

	priority := map[string]int{
		"appimage": 1,
		"tarball":  2,
		"zip":      3,
	}

	archVariants := []string{arch}
	if arch == "x86_64" {
		archVariants = append(archVariants, "amd64")
	} else if arch == "aarch64" {
		archVariants = append(archVariants, "arm64")
	}

	allArchs := []string{"x86_64", "amd64", "aarch64", "arm64", "armv7l", "arm", "i686", "386"}

	for _, asset := range release.Assets {
		name := asset.Name
		lowerName := strings.ToLower(name)

		unwanted := []string{"node_modules", "source", "src", "dev", "debug", "symbols", "test", "tests", "unittest", "example", "demo", "docs", "doc", "man", "manual", "changelog", "changes"}
		skip := false
		for _, pat := range unwanted {
			if strings.Contains(lowerName, pat) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		if !isValidLinuxAsset(name) {
			continue
		}

		containsOtherArch := false
		for _, a := range allArchs {
			if strings.Contains(lowerName, a) {
				isVariant := false
				for _, v := range archVariants {
					if a == v {
						isVariant = true
						break
					}
				}
				if !isVariant {
					containsOtherArch = true
					break
				}
			}
		}
		if containsOtherArch {
			debugPrintf("Asset %s skipped: other architecture", name)
			continue
		}

		var assetType string
		var matchesArch bool
		var isArchSpecific bool

		isAppImage := strings.HasSuffix(lowerName, ".appimage")
		switch {
		case isAppImage:
			assetType = "appimage"
			for _, v := range archVariants {
				if strings.Contains(lowerName, v) {
					matchesArch = true
					break
				}
			}
			isArchSpecific = matchesArch
		case strings.HasSuffix(lowerName, ".tar.gz"), strings.HasSuffix(lowerName, ".tgz"), strings.HasSuffix(lowerName, ".tar.xz"):
			assetType = "tarball"
			for _, v := range archVariants {
				if strings.Contains(lowerName, v) {
					matchesArch = true
					break
				}
			}
			if !matchesArch {
				debugPrintf("Asset %s added as generic (type=%s)", name, assetType)
				genericCandidates = append(genericCandidates, AssetInfo{URL: asset.BrowserDownloadURL, Type: assetType})
				continue
			}
			isArchSpecific = true
		case strings.HasSuffix(lowerName, ".zip"):
			assetType = "zip"
			for _, v := range archVariants {
				if strings.Contains(lowerName, v) {
					matchesArch = true
					break
				}
			}
			if matchesArch {
				isArchSpecific = true
			} else {
				continue
			}
		default:
			continue
		}

		if matchesArch && isArchSpecific {
			debugPrintf("Asset %s added as candidate (type=%s, arch=%s)", name, assetType, arch)
			candidates = append(candidates, AssetInfo{URL: asset.BrowserDownloadURL, Type: assetType})
		} else if assetType == "appimage" && !matchesArch {
			debugPrintf("Asset %s added as generic (type=%s)", name, assetType)
			genericCandidates = append(genericCandidates, AssetInfo{URL: asset.BrowserDownloadURL, Type: assetType})
		}
	}

	allCandidates := append(candidates, genericCandidates...)

	if len(allCandidates) == 0 {
		return "", nil
	}
	if len(allCandidates) == 1 {
		return allCandidates[0].URL, nil
	}

	sort.Slice(allCandidates, func(i, j int) bool {
		prioI := priority[allCandidates[i].Type]
		prioJ := priority[allCandidates[j].Type]
		if prioI == prioJ {
			return i < j
		}
		return prioI < prioJ
	})

	return "MULTIPLE", allCandidates
}

func DownloadAndInstall(assetURL, owner, repo, version, description string) (*db.PackageInfo, error) {
	debugPrintf("Downloading %s", assetURL)
	tmpFile, err := os.CreateTemp("", "giet-*.pkg")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpFile.Name())

	if !QuietMode && !VerboseMode {
		fmt.Println("Starting download...")
	}
	if err := downloadWithProgress(assetURL, tmpFile); err != nil {
		return nil, err
	}
	tmpFile.Close()

	lower := strings.ToLower(assetURL)
	var info *db.PackageInfo
	switch {
	case strings.HasSuffix(lower, ".appimage"):
		debugPrintf("Package type: appimage")
		binName, err := installAppImage(tmpFile.Name(), assetURL, repo, description)
		if err != nil {
			return nil, err
		}
		pkgName := extractPackageName(assetURL)
		info = &db.PackageInfo{
			Owner:       owner,
			Repo:        repo,
			URL:         fmt.Sprintf("https://github.com/%s/%s", owner, repo),
			AssetURL:    assetURL,
			Version:     version,
			PackageName: pkgName,
			BinName:     binName,
			InstallTime: time.Now(),
		}
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"), strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".tar"):
		debugPrintf("Package type: tarball")
		binName, err := installTarball(tmpFile.Name(), assetURL, owner, repo, description)
		if err != nil {
			return nil, err
		}
		pkgName := extractPackageName(assetURL)
		info = &db.PackageInfo{
			Owner:       owner,
			Repo:        repo,
			URL:         fmt.Sprintf("https://github.com/%s/%s", owner, repo),
			AssetURL:    assetURL,
			Version:     version,
			PackageName: pkgName,
			BinName:     binName,
			InstallTime: time.Now(),
		}
	case strings.HasSuffix(lower, ".zip"):
		debugPrintf("Package type: zip")
		binName, err := installZip(tmpFile.Name(), assetURL, owner, repo, description)
		if err != nil {
			return nil, err
		}
		pkgName := extractPackageName(assetURL)
		info = &db.PackageInfo{
			Owner:       owner,
			Repo:        repo,
			URL:         fmt.Sprintf("https://github.com/%s/%s", owner, repo),
			AssetURL:    assetURL,
			Version:     version,
			PackageName: pkgName,
			BinName:     binName,
			InstallTime: time.Now(),
		}
	default:
		return nil, fmt.Errorf("unsupported package type: %s", filepath.Ext(assetURL))
	}

	key := owner + "/" + repo
	if err := db.AddOrUpdate(key, *info); err != nil && !QuietMode {
		fmt.Println("Warning: could not record package in database:", err)
	}
	return info, nil
}

func detectFileType(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	header := make([]byte, 512)
	n, err := f.Read(header)
	if err != nil || n < 512 {
		return ""
	}
	if bytes.HasPrefix(header[257:263], []byte("ustar")) {
		return "tar"
	}
	if bytes.Equal(header[:2], []byte{0x1f, 0x8b}) {
		return "gzip"
	}
	if bytes.Equal(header[:6], []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}) {
		return "xz"
	}
	return ""
}

func InstallLocalFile(filePath, owner, repo, description string) (*db.PackageInfo, error) {
	if _, err := os.Stat(filePath); err != nil {
		return nil, fmt.Errorf("file not found: %w", err)
	}

	detectedType := detectFileType(filePath)
	lower := strings.ToLower(filePath)
	var binName string
	var pkgName string
	var err error

	switch {
	case detectedType == "tar" || detectedType == "gzip" || detectedType == "xz" ||
		strings.HasSuffix(lower, ".tar") || strings.HasSuffix(lower, ".tar.gz") ||
		strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar.xz"):
		debugPrintf("Installing local tarball: %s", filePath)
		binName, err = installTarball(filePath, "", owner, repo, description)
		if err != nil {
			return nil, err
		}
		pkgName = binName
	case strings.HasSuffix(lower, ".appimage"):
		debugPrintf("Installing local AppImage: %s", filePath)
		tmpFile, err := os.CreateTemp("", "giet-*.appimage")
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmpFile.Name())
		src, err := os.Open(filePath)
		if err != nil {
			return nil, err
		}
		defer src.Close()
		if _, err := io.Copy(tmpFile, src); err != nil {
			return nil, err
		}
		tmpFile.Close()
		binName, err = installAppImage(tmpFile.Name(), "", repo, description)
		if err != nil {
			return nil, err
		}
		pkgName = binName
	case strings.HasSuffix(lower, ".zip"):
		debugPrintf("Installing local zip: %s", filePath)
		binName, err = installZip(filePath, "", owner, repo, description)
		if err != nil {
			return nil, err
		}
		pkgName = binName
	default:
		return nil, fmt.Errorf("unsupported local file type: %s (detected: %s)", filePath, detectedType)
	}

	if binName == "" {
		binName = repo
	}
	if pkgName == "" {
		pkgName = binName
	}

	info := &db.PackageInfo{
		Owner:         owner,
		Repo:          repo,
		URL:           "",
		AssetURL:      filePath,
		Version:       "local",
		PackageName:   pkgName,
		BinName:       binName,
		InstallTime:   time.Now(),
		LockedVersion: "",
	}
	return info, nil
}

func capitalize(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func CleanBinaryName(name string) string {
	suffixes := []string{
		"-linux", "-desktop", "-bin", "-cli",
		"-x86_64", "-amd64", "-x64", "-i686", "-386",
		"-aarch64", "-arm64", "-armv7l", "-arm",
		"-musl", "-gnu", "-unknown",
	}
	for _, suf := range suffixes {
		if strings.HasSuffix(name, suf) {
			name = strings.TrimSuffix(name, suf)
		}
	}
	re := regexp.MustCompile(`-\d+\.\d+.*$`)
	name = re.ReplaceAllString(name, "")
	name = strings.ToLower(name)
	return name
}

func findBestIcon(rootDir, binName string) string {
	var bestPath string
	var bestScore int

	priorityPaths := []string{
		filepath.Join("usr", "share", "icons", "hicolor", "256x256", "apps"),
		filepath.Join("usr", "share", "icons", "hicolor", "128x128", "apps"),
		filepath.Join("usr", "share", "icons", "hicolor", "64x64", "apps"),
		filepath.Join("usr", "share", "icons", "hicolor", "48x48", "apps"),
		filepath.Join("usr", "share", "icons", "hicolor", "32x32", "apps"),
		filepath.Join("usr", "share", "icons", "hicolor", "16x16", "apps"),
		"usr/share/icons",
		"share/icons",
		"icons",
		"usr/share/pixmaps",
		"share/pixmaps",
		"usr/share/applications",
	}

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".png" && ext != ".svg" && ext != ".xpm" {
			return nil
		}

		score := 0
		base := strings.ToLower(filepath.Base(path))
		if strings.Contains(base, strings.ToLower(binName)) {
			score += 10
		}
		for i, p := range priorityPaths {
			if strings.Contains(path, p) {
				score += (len(priorityPaths) - i)
				break
			}
		}
		if strings.Contains(path, "256x256") {
			score += 5
		} else if strings.Contains(path, "128x128") {
			score += 4
		} else if strings.Contains(path, "64x64") {
			score += 3
		} else if strings.Contains(path, "48x48") {
			score += 2
		} else if strings.Contains(path, "32x32") {
			score += 1
		}
		if ext == ".png" {
			score += 3
		} else if ext == ".svg" {
			score += 2
		} else if ext == ".xpm" {
			score += 1
		}
		if strings.Contains(path, "apps") {
			score += 2
		}

		if score > bestScore {
			bestScore = score
			bestPath = path
		}
		return nil
	})

	if err != nil {
		return ""
	}

	if bestPath == "" {
		var desktopFile string
		filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if strings.HasSuffix(strings.ToLower(path), ".desktop") {
				desktopFile = path
				return filepath.SkipAll
			}
			return nil
		})
		if desktopFile != "" {
			data, err := os.ReadFile(desktopFile)
			if err == nil {
				lines := strings.Split(string(data), "\n")
				for _, line := range lines {
					if strings.HasPrefix(line, "Icon=") {
						iconName := strings.TrimPrefix(line, "Icon=")
						if !strings.Contains(iconName, "/") {
							return iconName
						}
						if filepath.IsAbs(iconName) {
							if _, err := os.Stat(iconName); err == nil {
								return iconName
							}
						} else {
							candidate := filepath.Join(rootDir, iconName)
							if _, err := os.Stat(candidate); err == nil {
								return candidate
							}
						}
						var found string
						filepath.Walk(rootDir, func(p string, info os.FileInfo, err error) error {
							if err != nil || info.IsDir() {
								return nil
							}
							base := filepath.Base(p)
							if strings.Contains(strings.ToLower(base), strings.ToLower(iconName)) {
								ext := strings.ToLower(filepath.Ext(p))
								if ext == ".png" || ext == ".svg" || ext == ".xpm" {
									found = p
									return filepath.SkipAll
								}
							}
							return nil
						})
						if found != "" {
							return found
						}
						return iconName
					}
				}
			}
		}
	}

	return bestPath
}

func finalizeInstalledArchive(sourceDir, execPath, binName, repo, description string) (string, error) {
	shareDir := getShareDir()
	binDir := getBinDir()
	appDir := getApplicationsDir()
	iconsDir := getIconsDir()

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create bin directory: %w", err)
	}
	if err := os.MkdirAll(shareDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create share directory: %w", err)
	}
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create applications directory: %w", err)
	}
	if err := os.MkdirAll(iconsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create icons directory: %w", err)
	}

	targetShareDir := filepath.Join(shareDir, binName)
	if err := os.MkdirAll(targetShareDir, 0755); err != nil {
		return "", err
	}

	debugPrintf("Moving files from %s to %s", sourceDir, targetShareDir)

	var spinner *utils.Spinner
	if !VerboseMode && !QuietMode {
		spinner = utils.NewSpinner("Moving files")
	}

	err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == sourceDir {
			return nil
		}
		relPath, _ := filepath.Rel(sourceDir, path)
		dest := filepath.Join(targetShareDir, relPath)
		if info.IsDir() {
			return os.MkdirAll(dest, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if err := os.Rename(path, dest); err != nil {
			return copyFile(path, dest)
		}
		return nil
	})
	if err != nil {
		if spinner != nil {
			spinner.Stop()
		}
		return "", err
	}

	err = filepath.Walk(targetShareDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if err := os.Chmod(path, 0755); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if spinner != nil {
			spinner.Stop()
		}
		return "", fmt.Errorf("failed to set executable permissions: %w", err)
	}
	if spinner != nil {
		spinner.Stop()
	}

	relExecPath, err := filepath.Rel(sourceDir, execPath)
	if err != nil {
		return "", err
	}
	finalExecPath := filepath.Join(targetShareDir, relExecPath)

	wrapperPath := filepath.Join(binDir, binName)
	debugPrintf("Creating wrapper at %s", wrapperPath)
	wrapperContent := fmt.Sprintf(`#!/bin/sh
export LD_LIBRARY_PATH="%s${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
exec "%s" "$@"
`, targetShareDir, finalExecPath)

	if err := os.WriteFile(wrapperPath, []byte(wrapperContent), 0755); err != nil {
		return "", fmt.Errorf("failed to create wrapper script: %w", err)
	}

	createDesktop := AutoYes
	if !createDesktop && !QuietMode {
		fmt.Print("Create desktop entry? [y/N]: ")
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			resp := strings.ToLower(scanner.Text())
			debugPrintf("User input for desktop entry: '%s'", resp)
			if resp == "y" {
				createDesktop = true
			}
		} else {
			debugPrintf("Failed to read user input for desktop entry: %v", scanner.Err())
		}
	}
	if createDesktop {
		desktopTarget := filepath.Join(appDir, "giet-"+binName+".desktop")
		debugPrintf("Creating desktop entry at %s", desktopTarget)

		iconPath := findBestIcon(targetShareDir, binName)
		if iconPath != "" {
			if strings.Contains(iconPath, "/") {
				ext := filepath.Ext(iconPath)
				destIcon := filepath.Join(iconsDir, "hicolor", "256x256", "apps", binName+ext)
				if err := os.MkdirAll(filepath.Dir(destIcon), 0755); err == nil {
					data, err := os.ReadFile(iconPath)
					if err == nil {
						os.WriteFile(destIcon, data, 0644)
						iconPath = destIcon
					} else {
						iconPath = binName
					}
				} else {
					iconPath = binName
				}
			}
		} else {
			iconPath = binName
			if !QuietMode {
				fmt.Println(utils.Colorize(utils.ColorYellow, "No icon found, using icon name '"+binName+"' (may not appear)."))
			}
		}

		displayName := capitalize(binName)
		if description == "" {
			description = fmt.Sprintf("%s application", displayName)
		}

		desktopContent := fmt.Sprintf(`[Desktop Entry]
Name=%s
Comment=%s
Exec=%s
Icon=%s
Type=Application
Categories=Utility;
`, displayName, description, wrapperPath, iconPath)

		if err := os.WriteFile(desktopTarget, []byte(desktopContent), 0644); err != nil {
			fmt.Printf("Warning: could not create desktop entry: %v\n", err)
		} else if !QuietMode && !VerboseMode {
			fmt.Printf("Created desktop entry: %s\n", desktopTarget)
		}
	}

	if !QuietMode && !VerboseMode {
		fmt.Printf("Installed executable to %s (wrapper)\n", wrapperPath)
		fmt.Printf("Additional files installed to %s\n", targetShareDir)
	}
	return binName, nil
}

func installAppImage(tmpPath, assetURL, repo, description string) (string, error) {
	var baseName string
	if assetURL == "" {
		baseName = repo
	} else {
		filename := filepath.Base(assetURL)
		baseName = strings.TrimSuffix(filename, ".AppImage")
		baseName = strings.TrimSuffix(baseName, ".appimage")
		parts := strings.Split(baseName, "-")
		if len(parts) > 0 {
			baseName = parts[0]
		}
	}
	baseName = strings.ToLower(baseName)
	if baseName == "" {
		baseName = repo
	}
	debugPrintf("AppImage base name: %s", baseName)

	binDir := getBinDir()
	shareDir := getShareDir()
	appDir := getApplicationsDir()
	iconsDir := getIconsDir()

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create bin directory: %w", err)
	}
	if err := os.MkdirAll(shareDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create share directory: %w", err)
	}
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create applications directory: %w", err)
	}
	if err := os.MkdirAll(iconsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create icons directory: %w", err)
	}

	hasFUSE := false
	if _, err := os.Stat("/usr/lib/libfuse.so.2"); err == nil {
		hasFUSE = true
	} else if _, err := os.Stat("/lib/x86_64-linux-gnu/libfuse.so.2"); err == nil {
		hasFUSE = true
	} else if _, err := os.Stat("/usr/lib64/libfuse.so.2"); err == nil {
		hasFUSE = true
	} else {
		cmd := exec.Command("ldconfig", "-p")
		out, _ := cmd.Output()
		if strings.Contains(string(out), "libfuse.so.2") {
			hasFUSE = true
		}
	}
	debugPrintf("FUSE available: %v", hasFUSE)

	var destPath string
	var finalExtractDir string

	if hasFUSE {
		destPath = filepath.Join(binDir, baseName)
		if err := os.Rename(tmpPath, destPath); err != nil {
			if !QuietMode && !VerboseMode {
				spinner := utils.NewSpinner("Installing AppImage")
				src, err := os.Open(tmpPath)
				if err != nil {
					if spinner != nil {
						spinner.Stop()
					}
					return "", err
				}
				defer src.Close()
				dst, err := os.Create(destPath)
				if err != nil {
					if spinner != nil {
						spinner.Stop()
					}
					return "", err
				}
				defer dst.Close()
				_, err = io.Copy(dst, src)
				if err != nil {
					if spinner != nil {
						spinner.Stop()
					}
					return "", err
				}
				if err := os.Chmod(destPath, 0755); err != nil {
					if spinner != nil {
						spinner.Stop()
					}
					return "", err
				}
				if spinner != nil {
					spinner.Stop()
				}
				if !VerboseMode {
					fmt.Printf("Installed AppImage as %s\n", destPath)
				}
			} else {
				src, err := os.Open(tmpPath)
				if err != nil {
					return "", err
				}
				defer src.Close()
				dst, err := os.Create(destPath)
				if err != nil {
					return "", err
				}
				defer dst.Close()
				_, err = io.Copy(dst, src)
				if err != nil {
					return "", err
				}
				if err := os.Chmod(destPath, 0755); err != nil {
					return "", err
				}
			}
		} else {
			if err := os.Chmod(destPath, 0755); err != nil {
				return "", err
			}
			if !QuietMode && !VerboseMode {
				fmt.Printf("Installed AppImage as %s\n", destPath)
			}
		}

		metadataDir, err := os.MkdirTemp("", "appimage-metadata-*")
		if err != nil {
			return baseName, nil
		}
		defer os.RemoveAll(metadataDir)
		if err := os.Chmod(tmpPath, 0755); err == nil {
			var spinner *utils.Spinner
			if !VerboseMode && !QuietMode {
				spinner = utils.NewSpinner("Extracting metadata")
			}
			cmd := exec.Command(tmpPath, "--appimage-extract")
			cmd.Dir = metadataDir
			err := cmd.Run()
			if spinner != nil {
				spinner.Stop()
			}
			if err != nil {
				if !QuietMode {
					fmt.Println(utils.Colorize(utils.ColorYellow, "Could not extract metadata; icon may be missing."))
				}
			} else {
				squashfsRoot := filepath.Join(metadataDir, "squashfs-root")
				if _, err := os.Stat(squashfsRoot); err == nil {
					finalExtractDir = squashfsRoot
				}
			}
		}
	} else {
		if !QuietMode {
			fmt.Println(utils.Colorize(utils.ColorYellow, "FUSE not found. Extracting AppImage instead."))
			fmt.Println("This will use more disk space but does not require FUSE.")
		}

		if err := os.Chmod(tmpPath, 0755); err != nil {
			return "", fmt.Errorf("failed to make AppImage executable: %w", err)
		}

		extractDir := filepath.Join(shareDir, baseName)
		if err := os.MkdirAll(extractDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create extract directory: %w", err)
		}

		var spinner *utils.Spinner
		if !VerboseMode && !QuietMode {
			spinner = utils.NewSpinner("Extracting AppImage")
		}
		cmd := exec.Command(tmpPath, "--appimage-extract")
		cmd.Dir = extractDir
		output, err := cmd.CombinedOutput()
		if spinner != nil {
			spinner.Stop()
		}
		if err != nil {
			return "", fmt.Errorf("extraction failed: %w\n%s", err, output)
		}

		squashfsRoot := filepath.Join(extractDir, "squashfs-root")
		if _, err := os.Stat(squashfsRoot); err != nil {
			return "", fmt.Errorf("extraction did not produce squashfs-root directory")
		}

		finalExtractDir = squashfsRoot
		debugPrintf("Extracted AppImage to %s", finalExtractDir)

		appRunPath := filepath.Join(finalExtractDir, "AppRun")
		if _, err := os.Stat(appRunPath); err != nil {
			entries, err := os.ReadDir(finalExtractDir)
			if err != nil {
				return "", err
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				fullPath := filepath.Join(finalExtractDir, entry.Name())
				if info, err := os.Stat(fullPath); err == nil && info.Mode()&0111 != 0 {
					appRunPath = fullPath
					break
				}
			}
		}
		if appRunPath == "" {
			return "", fmt.Errorf("could not find AppRun or executable in extracted AppImage")
		}

		destPath = filepath.Join(binDir, baseName)
		wrapperContent := fmt.Sprintf(`#!/bin/sh
exec "%s" "$@"
`, appRunPath)
		if err := os.WriteFile(destPath, []byte(wrapperContent), 0755); err != nil {
			return "", fmt.Errorf("failed to create wrapper: %w", err)
		}

		if !QuietMode && !VerboseMode {
			fmt.Printf("Extracted AppImage to %s\n", finalExtractDir)
			fmt.Printf("Created wrapper script: %s\n", destPath)
		}
	}

	time.Sleep(200 * time.Millisecond)

	createDesktop := AutoYes
	if !createDesktop && !QuietMode {
		fmt.Print("Create desktop entry? [y/N]: ")
		var resp string
		fmt.Scanln(&resp)
		if resp == "y" || resp == "Y" {
			createDesktop = true
		}
	}
	if createDesktop {
		var iconSearchDir string
		if finalExtractDir != "" {
			iconSearchDir = finalExtractDir
		}

		var iconPath string
		if iconSearchDir != "" {
			iconPath = findBestIcon(iconSearchDir, baseName)
			if iconPath != "" {
				ext := filepath.Ext(iconPath)
				destIcon := filepath.Join(iconsDir, "hicolor", "256x256", "apps", baseName+ext)
				if err := os.MkdirAll(filepath.Dir(destIcon), 0755); err == nil {
					data, err := os.ReadFile(iconPath)
					if err == nil {
						os.WriteFile(destIcon, data, 0644)
						iconPath = destIcon
					}
				}
			}
		}

		if iconPath == "" {
			iconPath = baseName
			if !QuietMode {
				fmt.Println(utils.Colorize(utils.ColorYellow, "No icon found; using icon name '"+baseName+"' (may not appear)."))
			}
		}

		displayName := capitalize(baseName)
		if description == "" {
			description = fmt.Sprintf("%s application", displayName)
		}

		targetDesktop := filepath.Join(appDir, "giet-"+baseName+".desktop")
		desktopContent := fmt.Sprintf(`[Desktop Entry]
Name=%s
Comment=%s
Exec=%s
Icon=%s
Type=Application
Categories=Utility;
`, displayName, description, destPath, iconPath)

		if err := os.WriteFile(targetDesktop, []byte(desktopContent), 0644); err != nil {
			fmt.Printf("Warning: could not create desktop entry: %v\n", err)
		} else if !QuietMode && !VerboseMode {
			fmt.Printf("Created desktop entry: %s\n", targetDesktop)
		}
	}

	return baseName, nil
}

func safeJoin(base, sub string) (string, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	target := filepath.Join(absBase, sub)
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path traversal attempt: %s", sub)
	}
	return absTarget, nil
}

func installTarball(tmpPath, assetURL, owner, repo, description string) (string, error) {
	extractDir, err := os.MkdirTemp("", "giet-extract-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(extractDir)
	debugPrintf("Extracted to %s", extractDir)

	file, err := os.Open(tmpPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	header := make([]byte, 512)
	n, err := file.Read(header)
	if err != nil || n < 512 {
		return "", fmt.Errorf("failed to read file header: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return "", err
	}

	var tr *tar.Reader
	isGzip := bytes.Equal(header[:2], []byte{0x1f, 0x8b})
	isXz := bytes.Equal(header[:6], []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00})
	isPlainTar := bytes.HasPrefix(header[257:263], []byte("ustar"))

	var spinner *utils.Spinner
	if !VerboseMode && !QuietMode {
		spinner = utils.NewSpinner("Extracting archive")
	}

	if isGzip {
		gzr, err := gzip.NewReader(file)
		if err != nil {
			if spinner != nil {
				spinner.Stop()
			}
			return "", err
		}
		defer gzr.Close()
		tr = tar.NewReader(gzr)
	} else if isXz {
		xzr, err := xz.NewReader(file)
		if err != nil {
			if spinner != nil {
				spinner.Stop()
			}
			return "", err
		}
		tr = tar.NewReader(xzr)
	} else if isPlainTar {
		tr = tar.NewReader(file)
	} else {
		if spinner != nil {
			spinner.Stop()
		}
		return "", fmt.Errorf("unsupported archive format (not tar, gzip, or xz)")
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if spinner != nil {
				spinner.Stop()
			}
			return "", err
		}
		target, err := safeJoin(extractDir, hdr.Name)
		if err != nil {
			if spinner != nil {
				spinner.Stop()
			}
			return "", err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				if spinner != nil {
					spinner.Stop()
				}
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				if spinner != nil {
					spinner.Stop()
				}
				return "", err
			}
			outFile, err := os.Create(target)
			if err != nil {
				if spinner != nil {
					spinner.Stop()
				}
				return "", err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				if spinner != nil {
					spinner.Stop()
				}
				return "", err
			}
			outFile.Close()
			if err := os.Chmod(target, os.FileMode(hdr.Mode)); err != nil {
				if spinner != nil {
					spinner.Stop()
				}
				return "", err
			}
		}
	}
	if spinner != nil {
		spinner.Stop()
	}

	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return "", err
	}

	var rootDir string
	allUnderOneDir := true
	for _, entry := range entries {
		if entry.IsDir() {
			if rootDir == "" {
				rootDir = entry.Name()
			} else if entry.Name() != rootDir {
				allUnderOneDir = false
				break
			}
		} else {
			allUnderOneDir = false
			break
		}
	}

	sourceDir := extractDir
	if allUnderOneDir && rootDir != "" {
		sourceDir = filepath.Join(extractDir, rootDir)
		if _, err := os.Stat(sourceDir); err != nil {
			return "", fmt.Errorf("expected root directory %s not found", rootDir)
		}
	}
	debugPrintf("Source directory: %s", sourceDir)

	execPath, err := findExecutable(sourceDir, repo)
	if err != nil {
		return "", err
	}
	debugPrintf("Executable: %s", execPath)

	if err := checkGlibcCompatibility(execPath); err != nil {
		return "", err
	}

	rawBinName := filepath.Base(execPath)
	binName := CleanBinaryName(rawBinName)
	if binName == "" {
		binName = repo
	}
	debugPrintf("Clean binary name: %s", binName)

	return finalizeInstalledArchive(sourceDir, execPath, binName, repo, description)
}

func installZip(tmpPath, assetURL, owner, repo, description string) (string, error) {
	extractDir, err := os.MkdirTemp("", "giet-extract-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(extractDir)
	debugPrintf("Extracted to %s", extractDir)

	var spinner *utils.Spinner
	if !VerboseMode && !QuietMode {
		spinner = utils.NewSpinner("Extracting archive")
	}
	r, err := zip.OpenReader(tmpPath)
	if err != nil {
		if spinner != nil {
			spinner.Stop()
		}
		return "", err
	}
	defer r.Close()

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			if spinner != nil {
				spinner.Stop()
			}
			return "", err
		}
		target, err := safeJoin(extractDir, f.Name)
		if err != nil {
			rc.Close()
			if spinner != nil {
				spinner.Stop()
			}
			return "", err
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, f.Mode())
		} else {
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				rc.Close()
				if spinner != nil {
					spinner.Stop()
				}
				return "", err
			}
			outFile, err := os.Create(target)
			if err != nil {
				rc.Close()
				if spinner != nil {
					spinner.Stop()
				}
				return "", err
			}
			_, err = io.Copy(outFile, rc)
			outFile.Close()
			rc.Close()
			if err != nil {
				if spinner != nil {
					spinner.Stop()
				}
				return "", err
			}
			if err := os.Chmod(target, f.Mode()); err != nil {
				if spinner != nil {
					spinner.Stop()
				}
				return "", err
			}
		}
	}
	if spinner != nil {
		spinner.Stop()
	}

	execPath, err := findExecutable(extractDir, repo)
	if err != nil {
		execPath = filepath.Join(extractDir, repo)
		if _, err := os.Stat(execPath); err != nil {
			return "", fmt.Errorf("no executable found in archive")
		}
	}
	debugPrintf("Executable found: %s", execPath)

	if err := checkGlibcCompatibility(execPath); err != nil {
		return "", err
	}

	rawBinName := filepath.Base(execPath)
	binName := CleanBinaryName(rawBinName)
	if binName == "" {
		binName = repo
	}
	debugPrintf("Clean binary name: %s", binName)

	return finalizeInstalledArchive(extractDir, execPath, binName, repo, description)
}

func findExecutable(rootDir, repo string) (string, error) {
	type candidate struct {
		path  string
		score int
	}
	var candidates []candidate

	ignoredNames := map[string]bool{
		"chrome-sandbox": true, "crashpad_handler": true, "chrome_crashpad_handler": true,
		"update": true, "updater": true, "glxtest": true, "test": true, "unittest": true,
		"ar-lib": true, "libtool": true, "compile": true, "depcomp": true,
		"missing": true, "install-sh": true, "mkinstalldirs": true,
		"configure": true, "config.guess": true, "config.sub": true,
		"ltmain.sh": true, "aclocal.m4": true, "autom4te.cache": true,
		"CMakeLists.txt": true, "Makefile": true, "Makefile.in": true,
		"README": true, "README.md": true, "LICENSE": true, "COPYING": true,
		"INSTALL": true, "AUTHORS": true, "ChangeLog": true, "NEWS": true,
		"run_all_tests.sh": true, "run_tests.sh": true, "test.sh": true,
		"check.sh": true, "tests.sh": true, "runtests.sh": true,
		"pingsender": true, "crashreporter": true, "plugin-container": true,
		"maintenanceservice": true, "firefox": true, "chrome": true,
	}

	isTestScript := func(name string) bool {
		lower := strings.ToLower(name)
		if lower == strings.ToLower(repo) {
			return false
		}
		patterns := []string{"test", "tests", "check", "run", "sample", "example", "demo"}
		for _, p := range patterns {
			if strings.Contains(lower, p) {
				return true
			}
		}
		return false
	}

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") || ignoredNames[strings.ToLower(base)] {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(base))
		if ext == ".so" || ext == ".dylib" || ext == ".dll" || ext == ".a" || ext == ".o" {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		header := make([]byte, 4)
		n, err := f.Read(header)
		if err != nil || n < 4 {
			f.Seek(0, 0)
			shebang := make([]byte, 2)
			n2, _ := f.Read(shebang)
			if n2 >= 2 && bytes.Equal(shebang, []byte("#!")) {
			} else {
				return nil
			}
		} else if !bytes.Equal(header, []byte{0x7f, 0x45, 0x4c, 0x46}) {
			f.Seek(0, 0)
			shebang := make([]byte, 2)
			n2, _ := f.Read(shebang)
			if n2 < 2 || !bytes.Equal(shebang, []byte("#!")) {
				return nil
			}
		}

		score := 0
		if strings.EqualFold(base, repo) {
			score += 100
		} else {
			cleaned := CleanBinaryName(repo)
			if strings.EqualFold(base, cleaned) {
				score += 80
			}
			if strings.Contains(strings.ToLower(base), strings.ToLower(repo)) {
				score += 50
			}
			if cleaned != repo && strings.Contains(strings.ToLower(base), strings.ToLower(cleaned)) {
				score += 30
			}
			relPath, _ := filepath.Rel(rootDir, path)
			if !strings.Contains(relPath, string(os.PathSeparator)) {
				score += 20
			}
		}

		if isTestScript(base) {
			score -= 50
		}

		candidates = append(candidates, candidate{path: path, score: score})
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no executable files found in archive")
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].path < candidates[j].path
	})

	return candidates[0].path, nil
}

func checkGlibcCompatibility(path string) error {
	if detect.HasGLIBC() {
		return nil
	}
	debugPrintf("Checking glibc compatibility for %s", path)
	f, err := elf.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var interp string
	for _, prog := range f.Progs {
		if prog.Type == elf.PT_INTERP {
			data := make([]byte, prog.Filesz)
			if _, err := prog.ReadAt(data, 0); err == nil {
				interp = string(data[:len(data)-1])
				break
			}
		}
	}
	if interp == "" {
		return nil
	}
	debugPrintf("ELF interpreter: %s", interp)
	if strings.Contains(interp, "ld-linux") {
		debugPrintf("Binary is glibc-linked, but system uses musl")
		return fmt.Errorf("binary requires glibc (interpreter %s), but system uses musl", interp)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Sync()
}

func extractPackageName(assetURL string) string {
	base := filepath.Base(assetURL)
	name := strings.TrimSuffix(base, ".appimage")
	name = strings.TrimSuffix(name, ".tar.gz")
	name = strings.TrimSuffix(name, ".tgz")
	name = strings.TrimSuffix(name, ".tar.xz")
	name = strings.TrimSuffix(name, ".tar")
	name = strings.TrimSuffix(name, ".zip")
	parts := strings.SplitN(name, "-", 2)
	return parts[0]
}

func checkDependency(cmdName, pkgName string) error {
	if _, err := exec.LookPath(cmdName); err == nil {
		return nil
	}
	fmt.Printf(utils.Colorize(utils.ColorYellow, "Missing dependency: %s\n"), cmdName)
	return fmt.Errorf("missing required dependency: %s. Please install it manually.", cmdName)
}

func cloneWithProgress(url, dest string) error {
	os.RemoveAll(dest)
	cmd := exec.Command("git", "clone", "--progress", url, dest)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var stderrOutput strings.Builder
	scanner := bufio.NewScanner(stderr)
	re := regexp.MustCompile(`(\d+)%`)
	var lastPercent int = -1
	for scanner.Scan() {
		line := scanner.Text()
		stderrOutput.WriteString(line + "\n")
		if matches := re.FindStringSubmatch(line); len(matches) > 1 {
			percent, _ := strconv.Atoi(matches[1])
			if percent != lastPercent {
				lastPercent = percent
				fmt.Printf("\rCloning: %d%%", percent)
			}
		}
		if strings.Contains(line, "Receiving objects:") && strings.Contains(line, "done") {
			fmt.Printf("\rCloning: 100%%\n")
		}
	}
	if err := scanner.Err(); err != nil {
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git clone failed: %w\n%s", err, stderrOutput.String())
	}
	fmt.Println()
	return nil
}

func CloneDefaultBranch(owner, repo string) (string, error) {
	if err := checkDependency("git", "git"); err != nil {
		return "", err
	}
	baseDir := "/tmp/giet"
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	dest := filepath.Join(baseDir, repo)
	os.RemoveAll(dest)
	url := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	if err := cloneWithProgress(url, dest); err != nil {
		return "", fmt.Errorf("git clone failed: %w", err)
	}
	return dest, nil
}

func FallbackInstall(repoPath, owner, repo string) (*db.PackageInfo, error) {
	cmdName := repo
	binDir := getBinDir()
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create bin directory: %w", err)
	}
	if path, err := exec.LookPath(cmdName); err == nil {
		return nil, fmt.Errorf("a command named '%s' already exists at %s. Remove it first or use a different name.", cmdName, path)
	}
	ignoreFiles := map[string]bool{
		"README": true, "README.md": true, "README.txt": true,
		"LICENSE": true, "LICENSE.txt": true, "LICENSE.md": true,
		".gitignore": true, ".git": true, ".github": true,
		"CONTRIBUTING.md": true, "CHANGELOG.md": true,
		"CODE_OF_CONDUCT.md": true, "Makefile": true, "Dockerfile": true,
		".travis.yml": true, ".gitmodules": true,
	}
	var candidates []string
	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if ignoreFiles[name] {
			continue
		}
		if entry.IsDir() {
			continue
		}
		fullPath := filepath.Join(repoPath, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		isExecPerm := (info.Mode() & 0111) != 0
		hasShebang := false
		if f, err := os.Open(fullPath); err == nil {
			header := make([]byte, 2)
			n, _ := f.Read(header)
			f.Close()
			if n >= 2 && bytes.Equal(header, []byte("#!")) {
				hasShebang = true
			}
		}
		if isExecPerm || hasShebang {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no executable or script files found in repository and no compatible release asset")
	}
	if len(candidates) > 1 {
		var match string
		for _, c := range candidates {
			if c == repo {
				match = c
				break
			}
		}
		if match != "" {
			candidates = []string{match}
		} else {
			return nil, fmt.Errorf("multiple executable/script files found (%v) – cannot determine which to install", candidates)
		}
	}
	execFile := candidates[0]
	srcPath := filepath.Join(repoPath, execFile)
	destPath := filepath.Join(binDir, execFile)
	spinner := utils.NewSpinner("Installing")
	cmd := exec.Command("cp", srcPath, destPath)
	output, err := cmd.CombinedOutput()
	spinner.Stop()
	if err != nil {
		fmt.Println(utils.Colorize(utils.ColorRed, "Command failed:"))
		fmt.Println(string(output))
		return nil, err
	}
	if err := os.Chmod(destPath, 0755); err != nil {
		return nil, err
	}
	fmt.Printf("Installed executable/script %s to %s\n", execFile, destPath)
	info := &db.PackageInfo{
		Owner:       owner,
		Repo:        repo,
		URL:         fmt.Sprintf("https://github.com/%s/%s", owner, repo),
		AssetURL:    "",
		Version:     "unknown",
		PackageName: execFile,
		BinName:     execFile,
		InstallTime: time.Now(),
	}
	key := owner + "/" + repo
	if err := db.AddOrUpdate(key, *info); err != nil {
		fmt.Println("Warning: could not record package in database:", err)
	}
	return info, nil
}
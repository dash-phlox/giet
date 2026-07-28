package installer

import (
    "archive/tar"
    "archive/zip"
    "bufio"
    "bytes"
    "compress/gzip"
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
var AutoYes = false

func SetQuiet(q bool) {
    QuietMode = q
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

func platformFilter(name string) bool {
    lower := strings.ToLower(name)
    if strings.Contains(lower, "linux") {
        return true
    }
    if strings.Contains(lower, "darwin") || strings.Contains(lower, "macos") || strings.Contains(lower, "mac") ||
        strings.Contains(lower, "windows") || strings.Contains(lower, "win32") || strings.Contains(lower, "win") {
        return false
    }
    return true
}

func isValidLinuxAsset(name string) bool {
    lower := strings.ToLower(name)
    forbiddenOS := []string{
        "darwin", "macos", "windows", "win32", "win64", "win",
        "freebsd", "openbsd", "netbsd", "dragonfly", "dragonflybsd",
        "haiku", "omnios", "solaris", "illumos",
    }
    for _, os := range forbiddenOS {
        if strings.Contains(lower, os) {
            return false
        }
    }
    if detect.HasGLIBC() {
        if strings.Contains(lower, "musl") {
            return false
        }
    } else {
        if strings.Contains(lower, "gnu") || strings.Contains(lower, "glibc") {
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
            return false
        }
    }
    return true
}

type AssetInfo struct {
    URL  string
    Type string
}

func FindAsset(release *github.GitHubRelease, distro, arch string) (string, []AssetInfo) {
    var candidates []AssetInfo
    var genericCandidates []AssetInfo

    priority := map[string]int{
        "rpm":      1,
        "appimage": 2,
        "tarball":  3,
        "zip":      4,
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

        isAppImage := strings.HasSuffix(lowerName, ".appimage")
        if !isAppImage {
            if !platformFilter(name) {
                continue
            }
        }

        if strings.HasSuffix(lowerName, ".deb") {
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
            continue
        }

        var assetType string
        var matchesArch bool
        var isArchSpecific bool

        switch {
        case strings.HasSuffix(lowerName, ".rpm"):
            if !detect.IsRPMFamily() {
                continue
            }
            assetType = "rpm"
            for _, v := range archVariants {
                if strings.Contains(lowerName, v) {
                    matchesArch = true
                    break
                }
            }
            isArchSpecific = true
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
            candidates = append(candidates, AssetInfo{URL: asset.BrowserDownloadURL, Type: assetType})
        } else if assetType == "appimage" && !matchesArch {
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

func DownloadAndInstall(assetURL, distro, owner, repo, version, description string) (*db.PackageInfo, error) {
    tmpFile, err := os.CreateTemp("", "giet-*.pkg")
    if err != nil {
        return nil, err
    }
    defer os.Remove(tmpFile.Name())

    if !QuietMode {
        fmt.Println("Starting download...")
    }
    if err := downloadWithProgress(assetURL, tmpFile); err != nil {
        return nil, err
    }
    tmpFile.Close()

    lower := strings.ToLower(assetURL)
    var info *db.PackageInfo
    switch {
    case strings.HasSuffix(lower, ".rpm"):
        pkgName, err := installRPM(tmpFile.Name())
        if err != nil {
            return nil, err
        }
        info = &db.PackageInfo{
            Owner:       owner,
            Repo:        repo,
            URL:         fmt.Sprintf("https://github.com/%s/%s", owner, repo),
            AssetURL:    assetURL,
            Version:     version,
            PackageName: pkgName,
            BinName:     pkgName,
            InstallTime: time.Now(),
        }
    case strings.HasSuffix(lower, ".appimage"):
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
    if bytes.Equal(header[:3], []byte("RPM")) {
        return "rpm"
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
    case detectedType == "rpm" || strings.HasSuffix(lower, ".rpm"):
        pkgName, err = installRPM(filePath)
        if err != nil {
            return nil, err
        }
        binName = pkgName
    case detectedType == "tar" || detectedType == "gzip" || detectedType == "xz" ||
        strings.HasSuffix(lower, ".tar") || strings.HasSuffix(lower, ".tar.gz") ||
        strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar.xz"):
        binName, err = installTarball(filePath, "", owner, repo, description)
        if err != nil {
            return nil, err
        }
        pkgName = binName
    case strings.HasSuffix(lower, ".appimage"):
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

func installRPM(pkgPath string) (string, error) {
    if _, err := os.Stat(pkgPath); err != nil {
        return "", fmt.Errorf("package file not found: %w", err)
    }
    absPath, err := filepath.Abs(pkgPath)
    if err != nil {
        return "", err
    }
    nameCmd := exec.Command("rpm", "-qp", "--qf", "%{NAME}", absPath)
    nameOut, err := nameCmd.Output()
    if err != nil {
        return "", fmt.Errorf("failed to query RPM package name: %w", err)
    }
    pkgName := strings.TrimSpace(string(nameOut))
    if pkgName == "" {
        return "", fmt.Errorf("could not determine RPM package name")
    }

    spinner := utils.NewSpinner("Installing")
    cmd := exec.Command("rpm", "-ivh", absPath)
    output, err := cmd.CombinedOutput()
    spinner.Stop()
    if err != nil {
        outputStr := string(output)
        if strings.Contains(outputStr, "already installed") {
            fmt.Println(utils.Colorize(utils.ColorYellow, "Package already installed."))
            return pkgName, nil
        }
        if strings.Contains(outputStr, "conflicts with file from package") {
            lines := strings.Split(outputStr, "\n")
            var conflictLine string
            for _, line := range lines {
                if strings.Contains(line, "conflicts with file from package") {
                    conflictLine = strings.TrimSpace(line)
                    break
                }
            }
            if conflictLine == "" {
                conflictLine = strings.TrimSpace(outputStr)
            }
            re := regexp.MustCompile(`from package (\S+)`)
            matches := re.FindStringSubmatch(conflictLine)
            conflictingPkg := ""
            if len(matches) > 1 {
                conflictingPkg = matches[1]
            }
            fmt.Println(utils.Colorize(utils.ColorRed, "File conflict detected:"))
            fmt.Println(conflictLine)
            fmt.Println()
            fmt.Println("Options:")
            fmt.Println("  1) Overwrite conflicting files")
            if conflictingPkg != "" {
                fmt.Printf("  2) Remove conflicting package [%s] and continue\n", conflictingPkg)
            } else {
                fmt.Println("  2) Remove conflicting package and continue")
            }
            fmt.Println("  3) Cancel installation")
            fmt.Print("Choose an option [1/2/3]: ")
            var choice string
            fmt.Scanln(&choice)
            switch choice {
            case "1":
                fmt.Println("Overwriting conflicting files...")
                spinner := utils.NewSpinner("Installing (with replacefiles)")
                replaceCmd := exec.Command("rpm", "-ivh", "--replacefiles", absPath)
                replaceOut, replaceErr := replaceCmd.CombinedOutput()
                spinner.Stop()
                if replaceErr != nil {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Installation still failed:"))
                    replaceStr := string(replaceOut)
                    if strings.Contains(replaceStr, "conflicts with file from package") {
                        lines2 := strings.Split(replaceStr, "\n")
                        for _, line := range lines2 {
                            if strings.Contains(line, "conflicts with file from package") {
                                fmt.Println(line)
                                break
                            }
                        }
                    } else {
                        fmt.Println(replaceStr)
                    }
                    return "", replaceErr
                }
                fmt.Println(utils.Colorize(utils.ColorGreen, "Installation succeeded."))
                return pkgName, nil
            case "2":
                if conflictingPkg == "" {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Could not determine conflicting package name. Aborting."))
                    return "", fmt.Errorf("cannot remove conflicting package")
                }
                fmt.Printf("Removing conflicting package: %s\n", conflictingPkg)
                removeCmd := exec.Command("rpm", "-e", conflictingPkg)
                removeOut, removeErr := removeCmd.CombinedOutput()
                if removeErr != nil {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Failed to remove conflicting package:"))
                    fmt.Println(string(removeOut))
                    return "", removeErr
                }
                fmt.Println(utils.Colorize(utils.ColorGreen, "Conflicting package removed. Retrying installation..."))
                spinner := utils.NewSpinner("Installing")
                retryCmd := exec.Command("rpm", "-ivh", absPath)
                retryOut, retryErr := retryCmd.CombinedOutput()
                spinner.Stop()
                if retryErr != nil {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Installation failed after removing conflict:"))
                    fmt.Println(string(retryOut))
                    return "", retryErr
                }
                fmt.Println(utils.Colorize(utils.ColorGreen, "Installation succeeded."))
                return pkgName, nil
            default:
                fmt.Println("Installation cancelled.")
                return "", fmt.Errorf("installation cancelled by user")
            }
        }
        fmt.Println(utils.Colorize(utils.ColorRed, "Command failed:"))
        fmt.Println(outputStr)
        return "", err
    }
    return pkgName, nil
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
                        if strings.Contains(iconName, "/") {
                            if filepath.IsAbs(iconName) {
                                if _, err := os.Stat(iconName); err == nil {
                                    bestPath = iconName
                                    return bestPath
                                }
                            } else {
                                candidate := filepath.Join(rootDir, iconName)
                                if _, err := os.Stat(candidate); err == nil {
                                    bestPath = candidate
                                    return bestPath
                                }
                            }
                        } else {
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
                                bestPath = found
                                return bestPath
                            }
                        }
                    }
                }
            }
        }
    }

    return bestPath
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

    var destPath string
    var finalExtractDir string

    if hasFUSE {
        destPath = filepath.Join(binDir, baseName)
        if err := os.Rename(tmpPath, destPath); err != nil {
            if !QuietMode {
                spinner := utils.NewSpinner("Installing AppImage")
                src, err := os.Open(tmpPath)
                if err != nil {
                    spinner.Stop()
                    return "", err
                }
                defer src.Close()
                dst, err := os.Create(destPath)
                if err != nil {
                    spinner.Stop()
                    return "", err
                }
                defer dst.Close()
                _, err = io.Copy(dst, src)
                if err != nil {
                    spinner.Stop()
                    return "", err
                }
                if err := os.Chmod(destPath, 0755); err != nil {
                    spinner.Stop()
                    return "", err
                }
                spinner.Stop()
                fmt.Printf("Installed AppImage as %s\n", destPath)
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
            if !QuietMode {
                fmt.Printf("Installed AppImage as %s\n", destPath)
            }
        }

        metadataDir, err := os.MkdirTemp("", "appimage-metadata-*")
        if err != nil {
            return baseName, nil
        }
        defer os.RemoveAll(metadataDir)
        if err := os.Chmod(tmpPath, 0755); err == nil {
            spinner := utils.NewSpinner("Extracting metadata")
            cmd := exec.Command(tmpPath, "--appimage-extract")
            cmd.Dir = metadataDir
            err := cmd.Run()
            spinner.Stop()
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

        spinner := utils.NewSpinner("Extracting AppImage")
        cmd := exec.Command(tmpPath, "--appimage-extract")
        cmd.Dir = extractDir
        output, err := cmd.CombinedOutput()
        spinner.Stop()
        if err != nil {
            return "", fmt.Errorf("extraction failed: %w\n%s", err, output)
        }

        squashfsRoot := filepath.Join(extractDir, "squashfs-root")
        if _, err := os.Stat(squashfsRoot); err != nil {
            return "", fmt.Errorf("extraction did not produce squashfs-root directory")
        }

        finalExtractDir = squashfsRoot

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

        if !QuietMode {
            fmt.Printf("Extracted AppImage to %s\n", finalExtractDir)
            fmt.Printf("Created wrapper script: %s\n", destPath)
        }
    }

    time.Sleep(200 * time.Millisecond)

    if !detect.IsAndroid() {
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
            } else if !QuietMode {
                fmt.Printf("Created desktop entry: %s\n", targetDesktop)
            }
        }
    }

    return baseName, nil
}

func installTarball(tmpPath, assetURL, owner, repo, description string) (string, error) {
    extractDir, err := os.MkdirTemp("", "giet-extract-*")
    if err != nil {
        return "", err
    }
    defer os.RemoveAll(extractDir)

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

    spinner := utils.NewSpinner("Extracting archive")

    if isGzip {
        gzr, err := gzip.NewReader(file)
        if err != nil {
            spinner.Stop()
            return "", err
        }
        defer gzr.Close()
        tr = tar.NewReader(gzr)
    } else if isXz {
        xzr, err := xz.NewReader(file)
        if err != nil {
            spinner.Stop()
            return "", err
        }
        tr = tar.NewReader(xzr)
    } else if isPlainTar {
        tr = tar.NewReader(file)
    } else {
        spinner.Stop()
        return "", fmt.Errorf("unsupported archive format (not tar, gzip, or xz)")
    }

    for {
        hdr, err := tr.Next()
        if err == io.EOF {
            break
        }
        if err != nil {
            spinner.Stop()
            return "", err
        }
        target := filepath.Join(extractDir, hdr.Name)
        switch hdr.Typeflag {
        case tar.TypeDir:
            if err := os.MkdirAll(target, 0755); err != nil {
                spinner.Stop()
                return "", err
            }
        case tar.TypeReg:
            if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
                spinner.Stop()
                return "", err
            }
            outFile, err := os.Create(target)
            if err != nil {
                spinner.Stop()
                return "", err
            }
            if _, err := io.Copy(outFile, tr); err != nil {
                outFile.Close()
                spinner.Stop()
                return "", err
            }
            outFile.Close()
            if err := os.Chmod(target, os.FileMode(hdr.Mode)); err != nil {
                spinner.Stop()
                return "", err
            }
        }
    }
    spinner.Stop()

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

    execPath, err := findExecutable(sourceDir, repo)
    if err != nil {
        return "", err
    }

    if !detect.HasGLIBC() {
        fileCmd := exec.Command("file", execPath)
        out, _ := fileCmd.Output()
        outStr := string(out)
        if strings.Contains(outStr, "interpreter") && strings.Contains(outStr, "ld-linux") {
            fmt.Println(utils.Colorize(utils.ColorRed, "Error: The binary requires glibc, but your system uses musl."))
            return "", fmt.Errorf("incompatible binary (glibc on musl)")
        }
    }

    rawBinName := filepath.Base(execPath)
    binName := CleanBinaryName(rawBinName)
    if binName == "" {
        binName = repo
    }

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

    spinner = utils.NewSpinner("Moving files")
    err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
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
        spinner.Stop()
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
        spinner.Stop()
        return "", fmt.Errorf("failed to set executable permissions: %w", err)
    }
    spinner.Stop()

    relExecPath, _ := filepath.Rel(sourceDir, execPath)
    finalExecPath := filepath.Join(targetShareDir, relExecPath)

    wrapperPath := filepath.Join(binDir, binName)
    wrapperContent := fmt.Sprintf(`#!/bin/sh
export LD_LIBRARY_PATH="%s${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
exec "%s" "$@"
`, targetShareDir, finalExecPath)

    if err := os.WriteFile(wrapperPath, []byte(wrapperContent), 0755); err != nil {
        return "", fmt.Errorf("failed to create wrapper script: %w", err)
    }

    if !detect.IsAndroid() {
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
            desktopTarget := filepath.Join(appDir, "giet-"+binName+".desktop")

            iconPath := findBestIcon(targetShareDir, binName)
            if iconPath != "" {
                ext := filepath.Ext(iconPath)
                destIcon := filepath.Join(iconsDir, "hicolor", "256x256", "apps", binName+ext)
                if err := os.MkdirAll(filepath.Dir(destIcon), 0755); err == nil {
                    data, err := os.ReadFile(iconPath)
                    if err == nil {
                        os.WriteFile(destIcon, data, 0644)
                        iconPath = destIcon
                    }
                }
            } else {
                iconPath = binName
                if !QuietMode {
                    fmt.Println(utils.Colorize(utils.ColorYellow, "No icon found; using icon name '"+binName+"' (may not appear)."))
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
            } else if !QuietMode {
                fmt.Printf("Created desktop entry: %s\n", desktopTarget)
            }
        }
    }

    if !QuietMode {
        fmt.Printf("Installed executable to %s (wrapper)\n", wrapperPath)
        fmt.Printf("Additional files installed to %s\n", targetShareDir)
    }
    return binName, nil
}

func installZip(tmpPath, assetURL, owner, repo, description string) (string, error) {
    extractDir, err := os.MkdirTemp("", "giet-extract-*")
    if err != nil {
        return "", err
    }
    defer os.RemoveAll(extractDir)

    spinner := utils.NewSpinner("Extracting archive")
    r, err := zip.OpenReader(tmpPath)
    if err != nil {
        spinner.Stop()
        return "", err
    }
    defer r.Close()

    for _, f := range r.File {
        rc, err := f.Open()
        if err != nil {
            spinner.Stop()
            return "", err
        }
        target := filepath.Join(extractDir, f.Name)
        if f.FileInfo().IsDir() {
            os.MkdirAll(target, f.Mode())
        } else {
            if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
                rc.Close()
                spinner.Stop()
                return "", err
            }
            outFile, err := os.Create(target)
            if err != nil {
                rc.Close()
                spinner.Stop()
                return "", err
            }
            _, err = io.Copy(outFile, rc)
            outFile.Close()
            rc.Close()
            if err != nil {
                spinner.Stop()
                return "", err
            }
            if err := os.Chmod(target, f.Mode()); err != nil {
                spinner.Stop()
                return "", err
            }
        }
    }
    spinner.Stop()

    execPath, err := findExecutable(extractDir, repo)
    if err != nil {
        execPath = filepath.Join(extractDir, repo)
        if _, err := os.Stat(execPath); err != nil {
            return "", fmt.Errorf("no executable found in archive")
        }
    }

    if !detect.HasGLIBC() {
        fileCmd := exec.Command("file", execPath)
        out, _ := fileCmd.Output()
        outStr := string(out)
        if strings.Contains(outStr, "interpreter") && strings.Contains(outStr, "ld-linux") {
            fmt.Println(utils.Colorize(utils.ColorRed, "Error: The binary requires glibc, but your system uses musl."))
            return "", fmt.Errorf("incompatible binary (glibc on musl)")
        }
    }

    rawBinName := filepath.Base(execPath)
    binName := CleanBinaryName(rawBinName)
    if binName == "" {
        binName = repo
    }

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

    spinner = utils.NewSpinner("Moving files")
    err = filepath.Walk(extractDir, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return err
        }
        if path == extractDir {
            return nil
        }
        relPath, _ := filepath.Rel(extractDir, path)
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
        spinner.Stop()
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
        spinner.Stop()
        return "", fmt.Errorf("failed to set executable permissions: %w", err)
    }
    spinner.Stop()

    relExecPath, _ := filepath.Rel(extractDir, execPath)
    finalExecPath := filepath.Join(targetShareDir, relExecPath)

    wrapperPath := filepath.Join(binDir, binName)
    wrapperContent := fmt.Sprintf(`#!/bin/sh
export LD_LIBRARY_PATH="%s${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
exec "%s" "$@"
`, targetShareDir, finalExecPath)

    if err := os.WriteFile(wrapperPath, []byte(wrapperContent), 0755); err != nil {
        return "", fmt.Errorf("failed to create wrapper script: %w", err)
    }

    if !detect.IsAndroid() {
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
            desktopTarget := filepath.Join(appDir, "giet-"+binName+".desktop")

            iconPath := findBestIcon(targetShareDir, binName)
            if iconPath != "" {
                ext := filepath.Ext(iconPath)
                destIcon := filepath.Join(iconsDir, "hicolor", "256x256", "apps", binName+ext)
                if err := os.MkdirAll(filepath.Dir(destIcon), 0755); err == nil {
                    data, err := os.ReadFile(iconPath)
                    if err == nil {
                        os.WriteFile(destIcon, data, 0644)
                        iconPath = destIcon
                    }
                }
            } else {
                iconPath = binName
                if !QuietMode {
                    fmt.Println(utils.Colorize(utils.ColorYellow, "No icon found; using icon name '"+binName+"' (may not appear)."))
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
            } else if !QuietMode {
                fmt.Printf("Created desktop entry: %s\n", desktopTarget)
            }
        }
    }

    if !QuietMode {
        fmt.Printf("Installed executable to %s (wrapper)\n", wrapperPath)
        fmt.Printf("Additional files installed to %s\n", targetShareDir)
    }
    return binName, nil
}

func findExecutable(rootDir, repo string) (string, error) {
    var candidates []string
    var fallbackCandidates []string

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

    isRealExecutable := func(path string) bool {
        f, err := os.Open(path)
        if err != nil {
            return false
        }
        defer f.Close()
        header := make([]byte, 2)
        n, _ := f.Read(header)
        if n >= 2 && bytes.Equal(header, []byte("#!")) {
            base := filepath.Base(path)
            if isTestScript(base) {
                return false
            }
            return true
        }
        cmd := exec.Command("file", "--brief", "--mime-type", path)
        out, err := cmd.Output()
        if err != nil {
            return false
        }
        mime := strings.TrimSpace(string(out))
        if strings.HasPrefix(mime, "application/x-executable") ||
            strings.HasPrefix(mime, "application/x-sharedlib") ||
            strings.HasPrefix(mime, "application/x-pie-executable") {
            return true
        }
        cmd = exec.Command("file", path)
        out, err = cmd.Output()
        if err != nil {
            return false
        }
        if strings.Contains(string(out), "ELF") || strings.Contains(string(out), "Mach-O") {
            return true
        }
        return false
    }

    err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return err
        }
        if info.IsDir() {
            if info.Name() == "lib" || info.Name() == "plugins" || info.Name() == "resources" || info.Name() == "locales" {
                return filepath.SkipDir
            }
            return nil
        }
        if info.Mode()&0111 == 0 {
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

        if !isRealExecutable(path) {
            return nil
        }

        if strings.EqualFold(base, repo) {
            candidates = append([]string{path}, candidates...)
            return nil
        }
        if strings.Contains(strings.ToLower(base), strings.ToLower(repo)) && !isTestScript(base) {
            candidates = append(candidates, path)
            return nil
        }
        if ext == "" && !isTestScript(base) {
            fallbackCandidates = append(fallbackCandidates, path)
            return nil
        }
        if !isTestScript(base) {
            fallbackCandidates = append(fallbackCandidates, path)
        }
        return nil
    })
    if err != nil {
        return "", err
    }

    if len(candidates) > 0 {
        return candidates[0], nil
    }
    if len(fallbackCandidates) > 0 {
        return fallbackCandidates[0], nil
    }
    return "", fmt.Errorf("no executable files found in archive")
}

func hasShebang(path string) bool {
    f, err := os.Open(path)
    if err != nil {
        return false
    }
    defer f.Close()
    header := make([]byte, 2)
    n, _ := f.Read(header)
    return n >= 2 && bytes.Equal(header, []byte("#!"))
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
    name := strings.TrimSuffix(base, ".rpm")
    name = strings.TrimSuffix(name, ".appimage")
    name = strings.TrimSuffix(name, ".tar.gz")
    name = strings.TrimSuffix(name, ".tgz")
    name = strings.TrimSuffix(name, ".tar.xz")
    name = strings.TrimSuffix(name, ".tar")
    name = strings.TrimSuffix(name, ".zip")
    parts := strings.SplitN(name, "-", 2)
    return parts[0]
}

func RemovePackage(key string, distro string) error {
    data, err := db.Load()
    if err != nil {
        return err
    }
    pkg, exists := data[key]
    if !exists {
        return fmt.Errorf("package not found in giet database: %s", key)
    }
    assetURL := pkg.AssetURL
    binName := pkg.BinName
    if binName == "" {
        binName = pkg.PackageName
        if binName == "" {
            binName = pkg.Repo
        }
    }

    binDir := getBinDir()
    shareDir := getShareDir()
    appDir := getApplicationsDir()
    binPath := filepath.Join(binDir, binName)

    isAppImage := false
    if strings.HasSuffix(assetURL, ".appimage") || strings.HasSuffix(assetURL, ".AppImage") {
        isAppImage = true
    } else if assetURL == "" {
        if _, err := os.Stat(binPath); err == nil {
            fileCmd := exec.Command("file", binPath)
            fileOut, _ := fileCmd.Output()
            if strings.Contains(string(fileOut), "AppImage") {
                isAppImage = true
            }
        }
    }

    switch {
    case strings.HasSuffix(assetURL, ".rpm"):
        err := removeRPM(pkg.PackageName)
        if err != nil {
            if strings.Contains(err.Error(), "not installed") {
                fmt.Printf("RPM package not found, attempting to remove binary: %s\n", binPath)
                if e := os.Remove(binPath); e != nil {
                    return fmt.Errorf("failed to remove binary: %w", e)
                }
                fmt.Printf("Removed %s\n", binPath)
                return nil
            }
            return err
        }
        return nil
    case isAppImage:
        fmt.Println("Removing AppImage...")
        if _, err := os.Stat(binPath); err == nil {
            if err := os.Remove(binPath); err != nil {
                return fmt.Errorf("failed to remove %s: %w", binPath, err)
            }
            fmt.Printf("  Removed %s\n", binPath)
        } else {
            fmt.Printf("  Binary not found: %s\n", binPath)
        }
        desktopFiles, _ := filepath.Glob(filepath.Join(appDir, "*.desktop"))
        removedDesktop := false
        for _, df := range desktopFiles {
            data, err := os.ReadFile(df)
            if err != nil {
                continue
            }
            if strings.Contains(string(data), "Exec="+binPath) {
                if err := os.Remove(df); err != nil {
                    fmt.Printf("Warning: could not remove desktop entry %s: %v\n", df, err)
                } else {
                    fmt.Printf("  Removed desktop entry: %s\n", filepath.Base(df))
                    removedDesktop = true
                }
            }
        }
        if !removedDesktop {
            pattern := filepath.Join(appDir, "*"+binName+"*.desktop")
            matches, _ := filepath.Glob(pattern)
            for _, m := range matches {
                if err := os.Remove(m); err != nil {
                    fmt.Printf("Warning: could not remove desktop entry %s: %v\n", m, err)
                } else {
                    fmt.Printf("  Removed desktop entry (filename match): %s\n", filepath.Base(m))
                    removedDesktop = true
                }
            }
        }
        if !removedDesktop {
            fmt.Println("  No matching desktop entry found.")
        }
        homeDir := getHomeDir()
        if homeDir != "" {
            appsDir := filepath.Join(homeDir, "Applications")
            pattern := filepath.Join(appsDir, "*"+binName+"*")
            matches, _ := filepath.Glob(pattern)
            if len(matches) == 0 {
                fmt.Printf("  No files found in %s matching '%s'.\n", appsDir, "*"+binName+"*")
            } else {
                for _, m := range matches {
                    if err := os.Remove(m); err != nil {
                        fmt.Printf("Warning: could not remove %s: %v\n", m, err)
                    } else {
                        fmt.Printf("  Removed from ~/Applications: %s\n", filepath.Base(m))
                    }
                }
            }
        }
        return nil
    default:
        sharePath := filepath.Join(shareDir, binName)
        fmt.Println("Removing...")
        var removalErr error
        if _, e := os.Stat(binPath); e == nil {
            if err := os.Remove(binPath); err != nil {
                removalErr = err
            } else {
                fmt.Printf("  Removed %s\n", binPath)
            }
        }
        if _, e := os.Stat(sharePath); e == nil {
            if err := os.RemoveAll(sharePath); err != nil {
                removalErr = err
            } else {
                fmt.Printf("  Removed %s\n", sharePath)
            }
        }
        desktopFiles, _ := filepath.Glob(filepath.Join(appDir, "*.desktop"))
        removedDesktop := false
        for _, df := range desktopFiles {
            data, err := os.ReadFile(df)
            if err != nil {
                continue
            }
            if strings.Contains(string(data), "Exec="+binPath) {
                if err := os.Remove(df); err != nil {
                    fmt.Printf("Warning: could not remove desktop entry %s: %v\n", df, err)
                } else {
                    fmt.Printf("  Removed desktop entry: %s\n", filepath.Base(df))
                    removedDesktop = true
                }
            }
        }
        if !removedDesktop {
            pattern := filepath.Join(appDir, "*"+binName+"*.desktop")
            matches, _ := filepath.Glob(pattern)
            for _, m := range matches {
                if err := os.Remove(m); err != nil {
                    fmt.Printf("Warning: could not remove desktop entry %s: %v\n", m, err)
                } else {
                    fmt.Printf("  Removed desktop entry (filename match): %s\n", filepath.Base(m))
                    removedDesktop = true
                }
            }
        }
        if !removedDesktop {
            fmt.Println("  No matching desktop entry found.")
        }
        if removalErr != nil {
            return removalErr
        }
        return nil
    }
}

func removeRPM(pkgName string) error {
    return runCmdSilentWithMessage("Removing", "rpm", "-e", pkgName)
}

func runCmdSilentWithMessage(message, name string, args ...string) error {
    spinner := utils.NewSpinner(message)
    cmd := exec.Command(name, args...)
    output, err := cmd.CombinedOutput()
    spinner.Stop()
    if err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Command failed:"))
        fmt.Println(string(output))
        return err
    }
    return nil
}

func checkDependency(cmdName, pkgName string) error {
    if _, err := exec.LookPath(cmdName); err == nil {
        return nil
    }
    if detect.IsAndroid() {
        fmt.Printf(utils.Colorize(utils.ColorYellow, "Missing dependency: %s\n"), cmdName)
        fmt.Printf("Would you like to install it via 'pkg install %s'? [y/N]: ", pkgName)
        var resp string
        fmt.Scanln(&resp)
        if resp == "y" || resp == "Y" {
            spinner := utils.NewSpinner(fmt.Sprintf("Installing %s", pkgName))
            cmd := exec.Command("pkg", "install", "-y", pkgName)
            output, err := cmd.CombinedOutput()
            spinner.Stop()
            if err != nil {
                fmt.Println(utils.Colorize(utils.ColorRed, "Failed to install "+pkgName))
                fmt.Println(string(output))
                return err
            }
            fmt.Printf(utils.Colorize(utils.ColorGreen, "Successfully installed %s\n"), pkgName)
            return nil
        }
        return fmt.Errorf("missing required dependency: %s", cmdName)
    }
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
    var baseDir string
    if detect.IsAndroid() {
        cwd, err := os.Getwd()
        if err != nil {
            return "", err
        }
        baseDir = filepath.Join(cwd, ".giet-tmp")
    } else {
        baseDir = "/tmp/giet"
    }
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
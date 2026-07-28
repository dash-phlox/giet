package main

import (
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"
    "strings"
    "time"

    "giet/pkg/db"
    "giet/pkg/detect"
    "giet/pkg/github"
    "giet/pkg/installer"
    "giet/pkg/utils"
)

const version = "0.4.0"

var (
    quiet        bool
    force        bool
    yes          bool
    installURLs  []string
    removePkgs   []string
    updatePkgs   []string
    updateAll    bool
    forceVersion string
    lockPkg      string
    unlockPkgs   []string
    listMode     bool
    showHelp     bool
    showVersion  bool
)

func getPrivilegeCommand() string {
    if _, err := exec.LookPath("sudo"); err == nil {
        return "sudo"
    }
    if _, err := exec.LookPath("doas"); err == nil {
        return "doas"
    }
    return ""
}

func main() {
    args := os.Args[1:]
    if len(args) == 0 {
        showHelp = true
    } else {
        if args[0] == "__complete" {
            runComplete()
            return
        }
        parseArgs(args)
    }

    if detect.IsNixOS() {
        fmt.Println(utils.Colorize(utils.ColorRed, "Warning: Giet is not officially supported on NixOS; packages may not install correctly."))
    }

    if showVersion {
        arch := detect.GetArch()
        fmt.Printf("giet %s (%s)\n", version, arch)
        os.Exit(0)
    }

    if showHelp {
        printHelp()
        os.Exit(0)
    }

    if listMode {
        runList()
        return
    }

    actions := 0
    if len(installURLs) > 0 {
        actions++
    }
    if len(removePkgs) > 0 {
        actions++
    }
    if len(updatePkgs) > 0 || updateAll {
        actions++
    }
    if lockPkg != "" {
        actions++
    }
    if len(unlockPkgs) > 0 {
        actions++
    }
    if actions != 1 {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: exactly one of -i, -r, -u, --lock, --unlock must be specified"))
        fmt.Println("Use -h for help")
        os.Exit(1)
    }

    if forceVersion != "" && len(installURLs) == 0 {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: --force-version can only be used with --install"))
        os.Exit(1)
    }

    installer.SetQuiet(quiet)
    installer.AutoYes = yes

    switch {
    case len(installURLs) > 0:
        for _, arg := range installURLs {
            if isLocalFile(arg) {
                runInstallLocal(arg)
            } else if strings.Contains(arg, "github.com") {
                runInstall(arg, false)
            } else if strings.Contains(arg, "/") && !strings.Contains(arg, ".") {
                runInstall("https://github.com/"+arg, false)
            } else {
                if _, err := os.Stat(arg); err == nil {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error: unsupported local file type. Supported: .tar.gz, .tgz, .tar.xz, .zip, .rpm, .tar"))
                } else {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error: install argument must be a GitHub URL, owner/repo, or a local file"))
                }
                os.Exit(1)
            }
        }
    case len(removePkgs) > 0:
        for _, pkg := range removePkgs {
            runRemove(pkg)
        }
    case len(updatePkgs) > 0:
        for _, pkg := range updatePkgs {
            runUpdate(pkg)
        }
    case updateAll:
        runUpdateAll()
    case lockPkg != "":
        runLock(lockPkg)
    case len(unlockPkgs) > 0:
        for _, pkg := range unlockPkgs {
            runUnlock(pkg)
        }
    }
}

func runComplete() {
    pkgs, err := db.List()
    if err != nil {
        os.Exit(1)
    }
    for key, info := range pkgs {
        fmt.Println(key)
        if info.BinName != "" && info.BinName != key && info.BinName != info.PackageName {
            fmt.Println(info.BinName)
        }
    }
    os.Exit(0)
}

func parseArgs(args []string) {
    i := 0
    for i < len(args) {
        arg := args[i]
        if !strings.HasPrefix(arg, "-") {
            fmt.Printf(utils.Colorize(utils.ColorRed, "Error: unexpected argument '%s'\n"), arg)
            fmt.Println("Use -h for help")
            os.Exit(1)
        }
        if arg == "--" {
            i++
            break
        }
        if arg == "--help" || arg == "-h" {
            showHelp = true
            return
        }
        if arg == "--version" || arg == "-v" {
            showVersion = true
            return
        }

        if strings.HasPrefix(arg, "--") {
            switch arg {
            case "--quiet", "-q":
                quiet = true
            case "--yes", "-y":
                yes = true
            case "--force", "-f":
                force = true
            case "--list":
                listMode = true
            case "--install", "-i":
                i++
                for i < len(args) && !strings.HasPrefix(args[i], "-") {
                    installURLs = append(installURLs, args[i])
                    i++
                }
                i--
                if len(installURLs) == 0 {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error: -i requires at least one package URL or file path"))
                    fmt.Println("Use -h for help")
                    os.Exit(1)
                }
            case "--remove":
                i++
                for i < len(args) && !strings.HasPrefix(args[i], "-") {
                    removePkgs = append(removePkgs, args[i])
                    i++
                }
                i--
                if len(removePkgs) == 0 {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error: -r requires at least one package name"))
                    fmt.Println("Use -h for help")
                    os.Exit(1)
                }
            case "--update", "-u":
                i++
                if i < len(args) && !strings.HasPrefix(args[i], "-") {
                    for i < len(args) && !strings.HasPrefix(args[i], "-") {
                        updatePkgs = append(updatePkgs, args[i])
                        i++
                    }
                    i--
                } else {
                    updateAll = true
                    i--
                }
            case "--force-version":
                if i+1 >= len(args) {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error: --force-version requires an argument"))
                    fmt.Println("Use -h for help")
                    os.Exit(1)
                }
                forceVersion = args[i+1]
                i++
            case "--lock":
                if i+1 >= len(args) {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error: --lock requires a package name"))
                    fmt.Println("Use -h for help")
                    os.Exit(1)
                }
                lockPkg = args[i+1]
                i++
            case "--unlock":
                i++
                for i < len(args) && !strings.HasPrefix(args[i], "-") {
                    unlockPkgs = append(unlockPkgs, args[i])
                    i++
                }
                i--
                if len(unlockPkgs) == 0 {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error: --unlock requires at least one package name"))
                    fmt.Println("Use -h for help")
                    os.Exit(1)
                }
            default:
                fmt.Printf(utils.Colorize(utils.ColorRed, "Error: unknown flag '%s'\n"), arg)
                fmt.Println("Use -h for help")
                os.Exit(1)
            }
        } else {
            flagStr := arg[1:]
            knownMulti := map[string]bool{
                "fv": true,
                "ls": true,
            }
            if knownMulti[flagStr] {
                switch flagStr {
                case "fv":
                    if i+1 >= len(args) {
                        fmt.Println(utils.Colorize(utils.ColorRed, "Error: -fv requires an argument"))
                        fmt.Println("Use -h for help")
                        os.Exit(1)
                    }
                    forceVersion = args[i+1]
                    i++
                case "ls":
                    listMode = true
                }
            } else {
                needArg := false
                var argForFlag string
                hasUpdateShort := false
                for _, ch := range flagStr {
                    switch ch {
                    case 'h':
                        showHelp = true
                        return
                    case 'q':
                        quiet = true
                    case 'y':
                        yes = true
                    case 'f':
                        force = true
                    case 'v':
                        showVersion = true
                        return
                    case 'i':
                        needArg = true
                        argForFlag = "install"
                    case 'r':
                        needArg = true
                        argForFlag = "remove"
                    case 'l':
                        fmt.Println(utils.Colorize(utils.ColorRed, "Error: -l is not a valid flag. Did you mean -ls or --list?"))
                        fmt.Println("Use -h for help")
                        os.Exit(1)
                    case 'u':
                        hasUpdateShort = true
                    default:
                        fmt.Printf(utils.Colorize(utils.ColorRed, "Error: unknown flag '-%c'\n"), ch)
                        fmt.Println("Use -h for help")
                        os.Exit(1)
                    }
                }
                if hasUpdateShort {
                    if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
                        i++
                        for i < len(args) && !strings.HasPrefix(args[i], "-") {
                            updatePkgs = append(updatePkgs, args[i])
                            i++
                        }
                        i--
                    } else {
                        updateAll = true
                    }
                }
                if needArg {
                    if i+1 >= len(args) {
                        fmt.Printf(utils.Colorize(utils.ColorRed, "Error: -%c requires an argument\n"), argForFlag[0])
                        fmt.Println("Use -h for help")
                        os.Exit(1)
                    }
                    switch argForFlag {
                    case "install":
                        i++
                        for i < len(args) && !strings.HasPrefix(args[i], "-") {
                            installURLs = append(installURLs, args[i])
                            i++
                        }
                        i--
                        if len(installURLs) == 0 {
                            fmt.Println(utils.Colorize(utils.ColorRed, "Error: -i requires at least one package URL or file path"))
                            fmt.Println("Use -h for help")
                            os.Exit(1)
                        }
                    case "remove":
                        i++
                        for i < len(args) && !strings.HasPrefix(args[i], "-") {
                            removePkgs = append(removePkgs, args[i])
                            i++
                        }
                        i--
                        if len(removePkgs) == 0 {
                            fmt.Println(utils.Colorize(utils.ColorRed, "Error: -r requires at least one package name"))
                            fmt.Println("Use -h for help")
                            os.Exit(1)
                        }
                    }
                }
            }
        }
        i++
    }
}

func printHelp() {
    fmt.Println(`Giet - GitHub-Based Package Manager

Commands:
  -i,  --install <url|file...>     Install one or more packages (GitHub URL, owner/repo, or local file)
  -r,  --remove  <pkg...>          Uninstall one or more installed packages
  -u,  --update  [pkg...]          Update one or more packages, or all if none given
  --lock         <pkg>             Lock a package to its current version
  --unlock       <pkg...>          Remove the lock from one or more packages
  -ls, --list                      List installed packages via Giet

Options:
  -q,  --quiet                     Show minimal output
  -y,  --yes                       Auto-confirm prompts
  -f,  --force                     Force removal even if system removal fails (with -r)
  -fv, --force-version             Install specific version of a package (with -i)

Giet:
  -v,  --version                   Show giet version
  -h,  --help                      Show this help message`)
}

func isLocalFile(path string) bool {
    if strings.HasPrefix(path, "~/") {
        home, err := os.UserHomeDir()
        if err == nil {
            path = filepath.Join(home, path[2:])
        }
    }
    if !strings.Contains(path, "/") && !strings.Contains(path, ".") {
        return false
    }
    info, err := os.Stat(path)
    if err != nil {
        return false
    }
    if info.IsDir() {
        return false
    }
    supported := []string{".tar.gz", ".tgz", ".tar.xz", ".zip", ".rpm", ".tar"}
    lower := strings.ToLower(path)
    for _, ext := range supported {
        if strings.HasSuffix(lower, ext) {
            return true
        }
    }
    return false
}

func runInstallLocal(filePath string) {
    if strings.HasPrefix(filePath, "~/") {
        home, err := os.UserHomeDir()
        if err == nil {
            filePath = filepath.Join(home, filePath[2:])
        }
    }
    base := filepath.Base(filePath)
    repo := strings.TrimSuffix(base, filepath.Ext(base))
    if strings.HasSuffix(repo, ".tar") && strings.HasSuffix(base, ".tar.gz") {
        repo = strings.TrimSuffix(repo, ".tar")
    }
    if repo == "" {
        repo = "local-package"
    }

    if !quiet {
        fmt.Printf("Installing local package: %s\n", filePath)
    }

    if !yes {
        fmt.Printf("Install local package %s? [y/N]: ", repo)
        var resp string
        fmt.Scanln(&resp)
        if resp != "y" && resp != "Y" {
            fmt.Println("Aborted.")
            return
        }
    }

    info, err := installer.InstallLocalFile(filePath, "local", repo, "")
    if err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: installation failed: "+err.Error()))
        os.Exit(1)
    }

    key := "local/" + repo
    if err := db.AddOrUpdate(key, *info); err != nil {
        fmt.Println(utils.Colorize(utils.ColorYellow, "Warning: could not record package in database: "+err.Error()))
    }

    fmt.Println(utils.Colorize(utils.ColorGreen, "Installation complete."))
}

func systemPackageSearch(pkgName string) bool {
    if detect.IsRPMFamily() {
        if !quiet {
            fmt.Printf("Searching for '%s' in dnf repositories...\n", pkgName)
        }
        cmd := exec.Command("dnf", "search", "--quiet", pkgName)
        output, err := cmd.Output()
        if err != nil {
            return false
        }
        return strings.Contains(string(output), pkgName)
    } else if detect.IsDebFamily() {
        if !quiet {
            fmt.Printf("Searching for '%s' in apt repositories...\n", pkgName)
        }
        cmd := exec.Command("apt-cache", "search", pkgName)
        output, err := cmd.Output()
        if err != nil {
            return false
        }
        return strings.Contains(string(output), pkgName)
    }
    return false
}

func systemPackageInstall(pkgName, owner, repo, key string) {
    if detect.IsRPMFamily() {
        fmt.Printf("Installing '%s' via dnf...\n", pkgName)
        cmd := exec.Command("sudo", "dnf", "install", "-y", pkgName)
        cmd.Stdout = os.Stdout
        cmd.Stderr = os.Stderr
        if err := cmd.Run(); err != nil {
            fmt.Println(utils.Colorize(utils.ColorRed, "dnf installation failed."))
            return
        }
        fmt.Println(utils.Colorize(utils.ColorGreen, "Package installed successfully via dnf."))
        versionCmd := exec.Command("rpm", "-q", pkgName)
        versionOut, err := versionCmd.Output()
        if err != nil {
            versionOut = []byte("unknown")
        }
        version := strings.TrimSpace(string(versionOut))
        cleanedName := installer.CleanBinaryName(pkgName)
        if cleanedName != pkgName {
            for _, dir := range []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin"} {
                binPath := filepath.Join(dir, pkgName)
                if _, err := os.Stat(binPath); err == nil {
                    symlinkPath := "/usr/local/bin/" + cleanedName
                    os.Remove(symlinkPath)
                    if err := os.Symlink(binPath, symlinkPath); err == nil {
                        fmt.Printf("Created symlink: %s -> %s\n", symlinkPath, binPath)
                    }
                    break
                }
            }
        }
        info := &db.PackageInfo{
            Owner:         owner,
            Repo:          repo,
            URL:           fmt.Sprintf("https://github.com/%s/%s", owner, repo),
            AssetURL:      "",
            Version:       version,
            PackageName:   pkgName,
            BinName:       cleanedName,
            InstallTime:   time.Now(),
            LockedVersion: "",
        }
        if err := db.AddOrUpdate(key, *info); err != nil && !quiet {
            fmt.Printf("Warning: could not record package in database: %v\n", err)
        }
    } else if detect.IsDebFamily() {
        fmt.Printf("Installing '%s' via apt...\n", pkgName)
        updateCmd := exec.Command("sudo", "apt-get", "update")
        updateCmd.Stdout = os.Stdout
        updateCmd.Stderr = os.Stderr
        updateCmd.Run()
        cmd := exec.Command("sudo", "apt-get", "install", "-y", pkgName)
        cmd.Stdout = os.Stdout
        cmd.Stderr = os.Stderr
        if err := cmd.Run(); err != nil {
            fmt.Println(utils.Colorize(utils.ColorRed, "apt installation failed."))
            return
        }
        fmt.Println(utils.Colorize(utils.ColorGreen, "Package installed successfully via apt."))
        versionCmd := exec.Command("dpkg-query", "-W", "-f=${Version}", pkgName)
        versionOut, err := versionCmd.Output()
        if err != nil {
            versionOut = []byte("unknown")
        }
        version := strings.TrimSpace(string(versionOut))
        cleanedName := installer.CleanBinaryName(pkgName)
        info := &db.PackageInfo{
            Owner:         owner,
            Repo:          repo,
            URL:           fmt.Sprintf("https://github.com/%s/%s", owner, repo),
            AssetURL:      "",
            Version:       version,
            PackageName:   pkgName,
            BinName:       cleanedName,
            InstallTime:   time.Now(),
            LockedVersion: "",
        }
        if err := db.AddOrUpdate(key, *info); err != nil && !quiet {
            fmt.Printf("Warning: could not record package in database: %v\n", err)
        }
    }
}

func runLock(pkg string) {
    pkgs, err := db.List()
    if err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }
    key := resolvePackageKey(pkg, pkgs)
    if key == "" {
        fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: no package found for '%s'. Use 'giet -ls' to see installed packages.", pkg)))
        os.Exit(1)
    }
    info := pkgs[key]
    if info.Version == "" {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: package has no recorded version. Reinstall to set a version."))
        os.Exit(1)
    }
    info.LockedVersion = info.Version
    if err := db.AddOrUpdate(key, info); err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }
    fmt.Printf(utils.Colorize(utils.ColorGreen, "Locked %s to current version %s\n"), key, info.Version)
}

func runUnlock(pkg string) {
    pkgs, err := db.List()
    if err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }
    key := resolvePackageKey(pkg, pkgs)
    if key == "" {
        fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: no package found for '%s'. Use 'giet -ls' to see installed packages.", pkg)))
        os.Exit(1)
    }
    info := pkgs[key]
    if info.LockedVersion == "" {
        fmt.Println(utils.Colorize(utils.ColorYellow, "Package is not locked."))
        return
    }
    info.LockedVersion = ""
    if err := db.AddOrUpdate(key, info); err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }
    fmt.Printf(utils.Colorize(utils.ColorGreen, "Unlocked %s\n"), key)
}

func runInstall(url string, isUpdate bool) {
    owner, repo, err := github.ParseRepoURL(url)
    if err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }
    key := owner + "/" + repo

    isAndroid := detect.IsAndroid()

    pkgs, err := db.List()
    if err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }
    existing, exists := pkgs[key]

    var release *github.GitHubRelease
    if forceVersion != "" {
        release, err = github.GetReleaseByTag(owner, repo, forceVersion)
        if err != nil {
            if strings.Contains(err.Error(), "404") {
                if !quiet {
                    fmt.Printf("Tag '%s' not found, trying 'v%s'\n", forceVersion, forceVersion)
                }
                release, err = github.GetReleaseByTag(owner, repo, "v"+forceVersion)
                if err != nil {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error: failed to fetch release for version "+forceVersion+" (tried with and without 'v'): "+err.Error()))
                    os.Exit(1)
                }
            } else {
                fmt.Println(utils.Colorize(utils.ColorRed, "Error: failed to fetch release for version "+forceVersion+": "+err.Error()))
                os.Exit(1)
            }
        }
        if !quiet {
            fmt.Printf("Forcing version: %s\n", release.TagName)
        }
    } else {
        if exists && existing.LockedVersion != "" {
            if !quiet {
                fmt.Printf(utils.Colorize(utils.ColorYellow, "Package %s is locked to version %s. Using locked version.\n"), key, existing.LockedVersion)
            }
            release, err = github.GetReleaseByTag(owner, repo, existing.LockedVersion)
            if err != nil {
                if strings.Contains(err.Error(), "404") {
                    release, err = github.GetReleaseByTag(owner, repo, "v"+existing.LockedVersion)
                }
                if err != nil {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error: failed to fetch locked version "+existing.LockedVersion+": "+err.Error()))
                    os.Exit(1)
                }
            }
        } else {
            release, err = github.GetLatestRelease(owner, repo)
            if err != nil {
                fmt.Println(utils.Colorize(utils.ColorRed, "Error: failed to fetch release: "+err.Error()))
                os.Exit(1)
            }
        }
    }

    description, _ := github.GetRepoInfo(owner, repo)

    if release.TagName == "HEAD" {
        fmt.Println(utils.Colorize(utils.ColorRed, "No prebuilt package found for this repository."))
        if !isAndroid {
            var resp string
            fmt.Print("Would you like to clone the repository and try to install the script/executable directly? [y/N]: ")
            fmt.Scanln(&resp)
            if resp == "y" || resp == "Y" {
                repoPath, err := installer.CloneDefaultBranch(owner, repo)
                if err != nil {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error cloning: "+err.Error()))
                    os.Exit(1)
                }
                _, err = installer.FallbackInstall(repoPath, owner, repo)
                if err != nil {
                    fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
                    os.Exit(1)
                }
                fmt.Println(utils.Colorize(utils.ColorGreen, "Installation complete (fallback)."))
                return
            }
            if systemPackageSearch(repo) {
                fmt.Printf("Package '%s' found in system repositories.\n", repo)
                fmt.Print("Would you like to install it via system package manager? [y/N]: ")
                fmt.Scanln(&resp)
                if resp == "y" || resp == "Y" {
                    systemPackageInstall(repo, owner, repo, key)
                    return
                }
            } else {
                fmt.Println("No matching package found in system repositories.")
            }
            fmt.Println(utils.Colorize(utils.ColorRed, "Installation failed."))
        } else {
            fmt.Println("No prebuilt package available for Android.")
        }
        return
    }

    if exists && existing.Version == release.TagName && release.TagName != "" {
        if isUpdate {
            if !quiet {
                fmt.Printf(utils.Colorize(utils.ColorYellow, "Package %s is already at the latest version %s. Skipping.\n"), key, release.TagName)
            }
            return
        } else {
            fmt.Printf(utils.Colorize(utils.ColorYellow, "Package %s is already at version %s.\n"), key, release.TagName)
            if !yes {
                fmt.Print("Reinstall? [y/N]: ")
                var resp string
                fmt.Scanln(&resp)
                if resp != "y" && resp != "Y" {
                    fmt.Println("Skipping.")
                    return
                }
            }
        }
    }

    arch := detect.GetArch()
    prettyName := detect.GetDisplayName()
    if !quiet {
        fmt.Printf("Detected system: %s / %s\n", prettyName, arch)
        fmt.Printf("Release: %s\n", release.TagName)
    }

    assetDistro := "linux"
    if detect.IsRPMFamily() {
        assetDistro = "fedora"
    } else if detect.IsDebFamily() {
        assetDistro = "debian"
    }

    assetResult, candidates := installer.FindAsset(release, assetDistro, arch)
    var selectedAsset string
    var userSelected bool

    if assetResult == "MULTIPLE" {
        fmt.Println(utils.Colorize(utils.ColorYellow, "Multiple compatible assets found:"))
        for i, cand := range candidates {
            parts := strings.Split(cand.URL, "/")
            filename := parts[len(parts)-1]
            fmt.Printf("  [%d] %s\n", i+1, filename)
        }
        fmt.Print("Select which one to install [1]: ")
        var choiceStr string
        fmt.Scanln(&choiceStr)
        choice := 1
        if choiceStr != "" {
            choice, err = strconv.Atoi(choiceStr)
            if err != nil || choice < 1 || choice > len(candidates) {
                fmt.Println(utils.Colorize(utils.ColorRed, "Invalid choice. Aborting."))
                os.Exit(1)
            }
        }
        selectedAsset = candidates[choice-1].URL
        userSelected = true
    } else if assetResult == "" {
        fmt.Println(utils.Colorize(utils.ColorYellow, "No compatible asset found in the latest stable release."))
        fmt.Println("Searching for a compatible asset in other releases (including prereleases)...")
        fallbackRelease, fallbackAsset, err := github.FindFirstReleaseWithCompatibleAsset(owner, repo, assetDistro, arch)
        if err != nil {
            fmt.Println(utils.Colorize(utils.ColorRed, "Error searching other releases: "+err.Error()))
        }
        if fallbackAsset != "" {
            fmt.Printf(utils.Colorize(utils.ColorYellow, "Found compatible asset in release %s (prerelease: %v).\n"), fallbackRelease.TagName, fallbackRelease.Prerelease)
            if !quiet {
                fmt.Printf("Found asset: %s\n", fallbackAsset)
            }
            fmt.Print("Would you like to install this version instead? [y/N]: ")
            var resp2 string
            fmt.Scanln(&resp2)
            if resp2 == "y" || resp2 == "Y" {
                selectedAsset = fallbackAsset
                userSelected = false
                release = fallbackRelease
            } else {
                fmt.Println("Using fallback to system package manager...")
                if !isAndroid {
                    if systemPackageSearch(repo) {
                        fmt.Printf("Package '%s' found in system repositories.\n", repo)
                        fmt.Print("Would you like to install it via system package manager? [y/N]: ")
                        var resp3 string
                        fmt.Scanln(&resp3)
                        if resp3 == "y" || resp3 == "Y" {
                            systemPackageInstall(repo, owner, repo, key)
                            return
                        }
                    } else {
                        fmt.Println("No matching package found in system repositories.")
                    }
                    fmt.Println(utils.Colorize(utils.ColorRed, "Installation failed."))
                } else {
                    fmt.Println("No prebuilt package available for Android.")
                }
                return
            }
        } else {
            fmt.Println(utils.Colorize(utils.ColorRed, "No prebuilt package found for this repository."))
            if !isAndroid {
                if systemPackageSearch(repo) {
                    fmt.Printf("Package '%s' found in system repositories.\n", repo)
                    fmt.Print("Would you like to install it via system package manager? [y/N]: ")
                    var resp4 string
                    fmt.Scanln(&resp4)
                    if resp4 == "y" || resp4 == "Y" {
                        systemPackageInstall(repo, owner, repo, key)
                        return
                    }
                } else {
                    fmt.Println("No matching package found in system repositories.")
                }
                fmt.Println(utils.Colorize(utils.ColorRed, "Installation failed."))
            } else {
                fmt.Println("No prebuilt package available for Android.")
            }
            return
        }
    } else {
        selectedAsset = assetResult
        userSelected = false
    }

    if !quiet && (userSelected || assetResult != "") && selectedAsset != "" {
        if userSelected {
            fmt.Printf("Selected asset: %s\n", selectedAsset)
        } else {
            fmt.Printf("Found asset: %s\n", selectedAsset)
        }
    }

    if !yes {
        action := "Install"
        if isUpdate {
            action = "Update"
        } else if exists && existing.Version == release.TagName {
            action = "Reinstall"
        }
        fmt.Printf("%s package %s? [y/N]: ", action, key)
        var resp5 string
        fmt.Scanln(&resp5)
        if resp5 != "y" && resp5 != "Y" {
            fmt.Println("Aborted.")
            return
        }
    }

    if !quiet {
        if isUpdate && exists && existing.Version != release.TagName {
            fmt.Printf("Updating %s from %s to %s\n", key, existing.Version, release.TagName)
        } else if exists && existing.Version == release.TagName {
            fmt.Printf("Reinstalling %s version %s\n", key, release.TagName)
        } else {
            fmt.Printf("Installing %s\n", key)
        }
    }

    _, err = installer.DownloadAndInstall(selectedAsset, assetDistro, owner, repo, release.TagName, description)
    if err != nil {
        if strings.Contains(err.Error(), "no executable files found in archive") {
            fmt.Println(utils.Colorize(utils.ColorRed, "The downloaded archive contains no executable file."))
            fmt.Println("This repository may only provide source code or auxiliary files.")
            if !isAndroid {
                if systemPackageSearch(repo) {
                    fmt.Printf("Package '%s' found in system repositories.\n", repo)
                    fmt.Print("Would you like to install it via system package manager? [y/N]: ")
                    var resp6 string
                    fmt.Scanln(&resp6)
                    if resp6 == "y" || resp6 == "Y" {
                        systemPackageInstall(repo, owner, repo, key)
                        return
                    }
                } else {
                    fmt.Println("No matching package found in system repositories.")
                }
                fmt.Println(utils.Colorize(utils.ColorRed, "Installation failed."))
            } else {
                fmt.Println("No prebuilt package available for Android.")
            }
            return
        }
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: installation failed: "+err.Error()))
        os.Exit(1)
    }

    fmt.Println(utils.Colorize(utils.ColorGreen, "Installation complete."))
}

func runUpdateAll() {
    pkgs, err := db.List()
    if err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }
    if len(pkgs) == 0 {
        fmt.Println("No packages installed.")
        return
    }
    for key, info := range pkgs {
        if info.LockedVersion != "" {
            if !quiet {
                fmt.Printf(utils.Colorize(utils.ColorYellow, "Skipping %s (locked to %s)\n"), key, info.LockedVersion)
            }
            continue
        }
        url := fmt.Sprintf("https://github.com/%s/%s", info.Owner, info.Repo)
        if !quiet {
            fmt.Printf("Updating %s...\n", key)
        }
        runInstall(url, true)
    }
}

func runRemove(arg string) {
    pkgs, err := db.List()
    if err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }

    key := resolvePackageKey(arg, pkgs)
    if key == "" {
        fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: no package found for '%s'. Use 'giet -ls' to see installed packages.", arg)))
        os.Exit(1)
    }

    info := pkgs[key]

    if !force && !yes {
        fmt.Printf("Are you sure you want to remove '%s'? [y/N]: ", key)
        var resp string
        fmt.Scanln(&resp)
        if resp != "y" && resp != "Y" {
            fmt.Println("Removal cancelled.")
            return
        }
    }

    if info.AssetURL == "" {
        if detect.IsRPMFamily() {
            fmt.Printf("Removing '%s' via dnf...\n", info.PackageName)
            cmd := exec.Command("sudo", "dnf", "remove", "-y", info.PackageName)
            cmd.Stdout = os.Stdout
            cmd.Stderr = os.Stderr
            err := cmd.Run()
            if err != nil {
                if force {
                    fmt.Println(utils.Colorize(utils.ColorYellow, "dnf removal failed, but --force was used. Removing database entry only."))
                } else {
                    fmt.Println(utils.Colorize(utils.ColorRed, "dnf removal failed."))
                    os.Exit(1)
                }
            } else {
                fmt.Println(utils.Colorize(utils.ColorGreen, "Package removed successfully via dnf."))
                symlinkPath := "/usr/local/bin/" + info.BinName
                if info.BinName != info.PackageName {
                    if _, e := os.Lstat(symlinkPath); e == nil {
                        if e := os.Remove(symlinkPath); e == nil {
                            fmt.Printf("Removed symlink: %s\n", symlinkPath)
                        }
                    }
                }
            }
        } else if detect.IsDebFamily() {
            fmt.Printf("Removing '%s' via apt...\n", info.PackageName)
            cmd := exec.Command("sudo", "apt-get", "remove", "-y", info.PackageName)
            cmd.Stdout = os.Stdout
            cmd.Stderr = os.Stderr
            err := cmd.Run()
            if err != nil {
                if force {
                    fmt.Println(utils.Colorize(utils.ColorYellow, "apt removal failed, but --force was used. Removing database entry only."))
                } else {
                    fmt.Println(utils.Colorize(utils.ColorRed, "apt removal failed."))
                    os.Exit(1)
                }
            } else {
                fmt.Println(utils.Colorize(utils.ColorGreen, "Package removed successfully via apt."))
            }
        }
        if err := db.Remove(key); err != nil {
            fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
            os.Exit(1)
        }
        fmt.Println(utils.Colorize(utils.ColorGreen, "Removal complete."))
        return
    }

    distroID := "fedora"
    if detect.IsDebFamily() {
        distroID = "debian"
    }
    if detect.IsAndroid() {
        distroID = "android"
    }
    err = installer.RemovePackage(key, distroID)
    if err != nil {
        if force {
            fmt.Println(utils.Colorize(utils.ColorYellow, "System removal failed, but --force was used. Removing database entry only."))
        } else {
            fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
            os.Exit(1)
        }
    }
    if err := db.Remove(key); err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }
    fmt.Println(utils.Colorize(utils.ColorGreen, "Removal complete."))
}

func runUpdate(arg string) {
    pkgs, err := db.List()
    if err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }

    key := resolvePackageKey(arg, pkgs)
    if key == "" {
        fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: no package found for '%s'. Use 'giet -ls' to see installed packages.", arg)))
        os.Exit(1)
    }

    info, exists := pkgs[key]
    if !exists {
        fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: package not found in database: %s", key)))
        os.Exit(1)
    }

    if info.LockedVersion != "" {
        fmt.Println(utils.Colorize(utils.ColorYellow, fmt.Sprintf("Package %s is locked to version %s. Unlock first to update.", key, info.LockedVersion)))
        return
    }

    if !yes {
        fmt.Printf("Update package %s to the latest version? [y/N]: ", key)
        var resp string
        fmt.Scanln(&resp)
        if resp != "y" && resp != "Y" {
            fmt.Println("Aborted.")
            return
        }
    }

    url := fmt.Sprintf("https://github.com/%s/%s", info.Owner, info.Repo)
    runInstall(url, true)
}

func runList() {
    pkgs, err := db.List()
    if err != nil {
        fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
        os.Exit(1)
    }
    if len(pkgs) == 0 {
        return
    }
    for key, info := range pkgs {
        lockStatus := ""
        if info.LockedVersion != "" {
            lockStatus = fmt.Sprintf(" (locked to %s)", info.LockedVersion)
        }
        fmt.Printf("  %s (package: %s, version %s, installed %s)%s\n", key, info.BinName, info.Version, info.InstallTime.Format("2006-01-02"), lockStatus)
    }
}

func resolvePackageKey(arg string, pkgs map[string]db.PackageInfo) string {
    if _, ok := pkgs[arg]; ok {
        return arg
    }

    if strings.Contains(arg, "github.com") {
        owner, repo, err := github.ParseRepoURL(arg)
        if err == nil {
            return owner + "/" + repo
        }
    }

    lowerArg := strings.ToLower(arg)
    for key, info := range pkgs {
        name := info.BinName
        if name == "" {
            name = info.PackageName
        }
        if strings.ToLower(name) == lowerArg {
            return key
        }
        parts := strings.Split(key, "/")
        if len(parts) == 2 && strings.ToLower(parts[1]) == lowerArg {
            return key
        }
    }
    return ""
}
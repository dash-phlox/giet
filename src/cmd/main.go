package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"giet/pkg/db"
	"giet/pkg/detect"
	"giet/pkg/github"
	"giet/pkg/installer"
	"giet/pkg/utils"

	"github.com/spf13/pflag"
)

const version = "0.7.2"

var (
	quiet        bool
	verbose      bool
	force        bool
	yes          bool
	installFlag  bool
	removeFlag   bool
	updateFlag   bool
	listFlag     bool
	lockFlag     bool
	unlockFlag   bool
	forceVersion string
	lockPkg      string
	showHelp     bool
	showVersion  bool
	selfUpdate   bool
)

func getBinDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin")
}

func getShareDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

func getApplicationsDir() string {
	return filepath.Join(getShareDir(), "applications")
}

func main() {
	fs := pflag.NewFlagSet("giet", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	fs.BoolVarP(&quiet, "quiet", "q", false, "Show minimal output")
	fs.BoolVarP(&verbose, "verbose", "v", false, "Enable verbose debug logging")
	fs.BoolVarP(&yes, "yes", "y", false, "Auto-confirm prompts")
	fs.BoolVarP(&force, "force", "f", false, "Force removal even if removal fails (with -r)")
	fs.StringVar(&forceVersion, "force-version", "", "Install specific version of a package (with -i)")

	fs.BoolVarP(&installFlag, "install", "i", false, "Install packages")
	fs.BoolVarP(&removeFlag, "remove", "r", false, "Remove packages")
	fs.BoolVarP(&updateFlag, "update", "u", false, "Update packages (all if no arguments)")
	fs.BoolVarP(&listFlag, "list", "l", false, "List installed packages")

	fs.StringVar(&lockPkg, "lock", "", "Lock a package to its current version")
	fs.BoolVar(&unlockFlag, "unlock", false, "Unlock packages")

	fs.BoolVarP(&showHelp, "help", "h", false, "Show this help message")
	fs.BoolVar(&selfUpdate, "self-update", false, "Update giet itself")
	fs.BoolVar(&showVersion, "version", false, "Show giet version")

	err := fs.Parse(os.Args[1:])
	if err != nil {
		fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
		fmt.Println("Run 'giet -h' for help.")
		os.Exit(1)
	}

	if selfUpdate {
		updated, err := runSelfUpdate()
		if err != nil {
			fmt.Println(utils.Colorize(utils.ColorRed, "Self-update failed: "+err.Error()))
			os.Exit(1)
		}
		if updated {
			fmt.Println(utils.Colorize(utils.ColorGreen, "Giet updated successfully."))
		}
		return
	}

	if fs.NFlag() == 0 && len(fs.Args()) == 0 {
		printHelp()
		os.Exit(0)
	}

	if showHelp {
		printHelp()
		os.Exit(0)
	}
	if showVersion {
		arch := detect.GetArch()
		fmt.Printf("giet %s (%s)\n", version, arch)
		os.Exit(0)
	}

	if detect.IsNixOS() {
		fmt.Println(utils.Colorize(utils.ColorRed, "Warning: Giet is not officially supported on NixOS; packages may not install correctly."))
	}

	if listFlag {
		runList()
		return
	}

	actions := 0
	if installFlag {
		actions++
	}
	if removeFlag {
		actions++
	}
	if updateFlag {
		actions++
	}
	if lockPkg != "" {
		actions++
	}
	if unlockFlag {
		actions++
	}
	if actions != 1 {
		fmt.Println(utils.Colorize(utils.ColorRed, "Error: exactly one of -i, -r, -u, --lock, --unlock must be specified"))
		fmt.Println("Run 'giet -h' for help.")
		os.Exit(1)
	}

	if forceVersion != "" && !installFlag {
		fmt.Println(utils.Colorize(utils.ColorRed, "Error: --force-version can only be used with --install"))
		fmt.Println("Run 'giet -h' for help.")
		os.Exit(1)
	}

	installer.SetQuiet(quiet)
	installer.SetVerbose(verbose)
	installer.AutoYes = yes

	args := fs.Args()

	switch {
	case installFlag:
		if len(args) == 0 {
			fmt.Println(utils.Colorize(utils.ColorRed, "Error: -i requires at least one package URL or file path"))
			fmt.Println("Run 'giet -h' for help.")
			os.Exit(1)
		}
		for _, arg := range args {
			if isLocalFile(arg) {
				runInstallLocal(arg)
			} else if strings.Contains(arg, "github.com") {
				runInstall(arg, false)
			} else if strings.Contains(arg, "/") && !strings.Contains(arg, ".") {
				runInstall("https://github.com/"+arg, false)
			} else {
				if _, err := os.Stat(arg); err == nil {
					fmt.Println(utils.Colorize(utils.ColorRed, "Error: unsupported local file type. Supported: .tar.gz, .tgz, .tar.xz, .zip, .tar, .appimage"))
				} else {
					fmt.Println(utils.Colorize(utils.ColorRed, "Error: install argument must be a GitHub URL, owner/repo, or a local file"))
				}
				os.Exit(1)
			}
		}
	case removeFlag:
		if len(args) == 0 {
			fmt.Println(utils.Colorize(utils.ColorRed, "Error: -r requires at least one package name"))
			fmt.Println("Run 'giet -h' for help.")
			os.Exit(1)
		}
		for _, pkg := range args {
			runRemove(pkg)
		}
	case updateFlag:
		if len(args) > 0 {
			for _, pkg := range args {
				runUpdate(pkg)
			}
		} else {
			runUpdateAll()
		}
	case lockPkg != "":
		runLock(lockPkg)
	case unlockFlag:
		if len(args) == 0 {
			fmt.Println(utils.Colorize(utils.ColorRed, "Error: --unlock requires at least one package name"))
			fmt.Println("Run 'giet -h' for help.")
			os.Exit(1)
		}
		for _, pkg := range args {
			runUnlock(pkg)
		}
	}
}

func printHelp() {
	fmt.Println(`Giet - GitHub-Based Package Manager

Commands:
  -i,  --install <url|file...>     Install one or more packages (GitHub URL, owner/repo, or local file)
  -r,  --remove  <pkg...>          Uninstall one or more installed packages
  -u,  --update  [pkg...]          Update one or more packages, or all if none given
       --lock    <pkg>             Lock a package to its current version
       --unlock  <pkg...>          Remove the lock from one or more packages
  -l,  --list                      List installed packages via Giet

Options:
  -q,  --quiet                     Show minimal output
  -v,  --verbose                   Show verbose (detailed) logging
  -y,  --yes                       Auto-confirm prompts
  -f,  --force                     Force removal even if removal fails (with -r)
  -fv, --force-version             Install specific version of a package (with -i)

Giet:
  -h,  --help                      Show this help message
       --self-update               Update giet itself
       --version                   Show giet version`)
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
	supported := []string{".tar.gz", ".tgz", ".tar.xz", ".zip", ".tar", ".appimage"}
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

func runLock(pkg string) {
	pkgs, err := db.List()
	if err != nil {
		fmt.Println(utils.Colorize(utils.ColorRed, "Error: "+err.Error()))
		os.Exit(1)
	}
	key := resolvePackageKey(pkg, pkgs)
	if key == "" {
		fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: no package found for '%s'. Use 'giet -l' to see installed packages.", pkg)))
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
		fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: no package found for '%s'. Use 'giet -l' to see installed packages.", pkg)))
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
			fmt.Println(utils.Colorize(utils.ColorGreen, "Installation complete."))
			return
		}
		fmt.Println(utils.Colorize(utils.ColorRed, "Installation cancelled."))
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
	if !quiet {
		fmt.Printf("Architecture: %s\n", arch)
		fmt.Printf("Release: %s\n", release.TagName)
	}

	assetResult, candidates := installer.FindAsset(release, arch)
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
		fallbackRelease, fallbackAsset, err := github.FindFirstReleaseWithCompatibleAsset(owner, repo, arch)
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
				fmt.Println(utils.Colorize(utils.ColorRed, "Installation cancelled."))
				return
			}
		} else {
			fmt.Println(utils.Colorize(utils.ColorRed, "No prebuilt package found for this repository."))
			fmt.Print("Would you like to clone the repository and try to install the script/executable directly? [y/N]: ")
			var resp string
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
				fmt.Println(utils.Colorize(utils.ColorGreen, "Installation complete."))
				return
			}
			fmt.Println(utils.Colorize(utils.ColorRed, "Installation cancelled."))
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

	if !yes && !userSelected {
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

	_, err = installer.DownloadAndInstall(selectedAsset, owner, repo, release.TagName, description)
	if err != nil {
		if strings.Contains(err.Error(), "no executable files found in archive") {
			fmt.Println(utils.Colorize(utils.ColorRed, "The downloaded archive contains no executable file."))
			fmt.Println("This repository may only provide source code or auxiliary files.")
			fmt.Print("Would you like to clone the repository and try to install the script/executable directly? [y/N]: ")
			var resp6 string
			fmt.Scanln(&resp6)
			if resp6 == "y" || resp6 == "Y" {
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
				fmt.Println(utils.Colorize(utils.ColorGreen, "Installation complete."))
				return
			}
			fmt.Println(utils.Colorize(utils.ColorRed, "Installation failed."))
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
		if info.Owner == "local" || info.URL == "" {
			if !quiet {
				fmt.Printf(utils.Colorize(utils.ColorYellow, "Skipping %s (local package, cannot update)\n"), key)
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
		fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: no package found for '%s'. Use 'giet -l' to see installed packages.", arg)))
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

	binName := info.BinName
	if binName == "" {
		binName = info.PackageName
	}
	if binName == "" {
		binName = info.Repo
	}

	binPath := filepath.Join(getBinDir(), binName)
	if _, err := os.Stat(binPath); err == nil {
		if err := os.Remove(binPath); err != nil {
			fmt.Printf("Warning: could not remove binary %s: %v\n", binPath, err)
		} else {
			fmt.Printf("Removed %s\n", binPath)
		}
	}

	sharePath := filepath.Join(getShareDir(), binName)
	if _, err := os.Stat(sharePath); err == nil {
		if err := os.RemoveAll(sharePath); err != nil {
			fmt.Printf("Warning: could not remove share directory %s: %v\n", sharePath, err)
		} else {
			fmt.Printf("Removed %s\n", sharePath)
		}
	}

	desktopPath := filepath.Join(getApplicationsDir(), "giet-"+binName+".desktop")
	if _, err := os.Stat(desktopPath); err == nil {
		if err := os.Remove(desktopPath); err != nil {
			fmt.Printf("Warning: could not remove desktop entry %s: %v\n", desktopPath, err)
		} else {
			fmt.Printf("Removed desktop entry: %s\n", desktopPath)
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
		fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: no package found for '%s'. Use 'giet -l' to see installed packages.", arg)))
		os.Exit(1)
	}

	info, exists := pkgs[key]
	if !exists {
		fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: package not found in database: %s", key)))
		os.Exit(1)
	}

	if info.Owner == "local" || info.URL == "" {
		fmt.Println(utils.Colorize(utils.ColorRed, fmt.Sprintf("Error: Package %s is a local package and cannot be updated.", key)))
		return
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

func getVersionFromSource(repoDir string) (string, error) {
	mainPath := filepath.Join(repoDir, "src", "cmd", "main.go")
	content, err := os.ReadFile(mainPath)
	if err != nil {
		return "", err
	}
	const prefix = `const version = "`
	start := strings.Index(string(content), prefix)
	if start == -1 {
		return "", fmt.Errorf("version constant not found")
	}
	start += len(prefix)
	end := strings.Index(string(content)[start:], `"`)
	if end == -1 {
		return "", fmt.Errorf("version constant value not closed")
	}
	return string(content)[start : start+end], nil
}

func compareVersions(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}
	for i := 0; i < maxLen; i++ {
		var vA, vB int
		if i < len(partsA) {
			vA, _ = strconv.Atoi(partsA[i])
		}
		if i < len(partsB) {
			vB, _ = strconv.Atoi(partsB[i])
		}
		if vA > vB {
			return 1
		}
		if vA < vB {
			return -1
		}
	}
	return 0
}

func downloadSourceTarball(destDir string) error {
	url := "https://github.com/dash-phlox/giet/archive/refs/heads/main.tar.gz"
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
	return nil
}

func runSelfUpdate() (bool, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return false, fmt.Errorf("go is required but not found in PATH.\nPlease install Go from your distro's package manager or from https://go.dev/dl")
	}

	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("failed to get executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return false, fmt.Errorf("failed to resolve symlinks: %w", err)
	}

	dir := filepath.Dir(exe)
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm()&0200 == 0 {
		if os.Geteuid() != 0 {
			fmt.Println("Insufficient permissions. Restarting with sudo...")
			cmd := exec.Command("sudo", append([]string{os.Args[0], "--self-update"}, os.Args[1:]...)...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			err := cmd.Run()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Self-update failed: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}
		return false, fmt.Errorf("directory %s is not writable", dir)
	}

	tempDir, err := os.MkdirTemp("", "giet-self-update-*")
	if err != nil {
		return false, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	repoDir := filepath.Join(tempDir, "giet")

	if _, err := exec.LookPath("git"); err == nil {
		if !quiet {
			fmt.Println("Cloning latest source with git...")
		}
		cmd := exec.Command("git", "clone", "--depth", "1", "https://github.com/dash-phlox/giet.git", repoDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return false, fmt.Errorf("git clone failed: %w", err)
		}
	} else {
		if !quiet {
			fmt.Println("git not found, downloading source tarball...")
		}
		if err := downloadSourceTarball(repoDir); err != nil {
			return false, fmt.Errorf("failed to download source tarball: %w", err)
		}
		entries, err := os.ReadDir(repoDir)
		if err != nil {
			return false, err
		}
		if len(entries) == 1 && entries[0].IsDir() {
			inner := filepath.Join(repoDir, entries[0].Name())
			innerEntries, err := os.ReadDir(inner)
			if err != nil {
				return false, err
			}
			for _, e := range innerEntries {
				src := filepath.Join(inner, e.Name())
				dst := filepath.Join(repoDir, e.Name())
				if err := os.Rename(src, dst); err != nil {
					if err := copyRecursive(src, dst); err != nil {
						return false, err
					}
				}
			}
			if err := os.RemoveAll(inner); err != nil {
				return false, err
			}
		}
	}

	modDir := filepath.Join(repoDir, "src")
	if _, err := os.Stat(modDir); err != nil {
		modDir = repoDir
	}
	modFile := filepath.Join(modDir, "go.mod")
	if _, err := os.Stat(modFile); err != nil {
		if !quiet {
			fmt.Printf("go.mod not found, running `go mod init giet` in %s\n", modDir)
		}
		initCmd := exec.Command("go", "mod", "init", "giet")
		initCmd.Dir = modDir
		initCmd.Stdout = os.Stdout
		initCmd.Stderr = os.Stderr
		if err := initCmd.Run(); err != nil {
			return false, fmt.Errorf("go mod init failed: %w", err)
		}
	}

	if !quiet {
		fmt.Printf("Running go mod tidy in %s\n", modDir)
	}
	tidyCmd := exec.Command("go", "mod", "tidy")
	tidyCmd.Dir = modDir
	tidyCmd.Stdout = os.Stdout
	tidyCmd.Stderr = os.Stderr
	if err := tidyCmd.Run(); err != nil {
		return false, fmt.Errorf("go mod tidy failed: %w", err)
	}

	remoteVersion, err := getVersionFromSource(repoDir)
	if err != nil {
		return false, fmt.Errorf("failed to read remote version: %w", err)
	}

	if compareVersions(remoteVersion, version) <= 0 {
		if !quiet {
			fmt.Println(utils.Colorize(utils.ColorYellow, fmt.Sprintf("Already at latest version %s", version)))
		}
		return false, nil
	}
	if !quiet {
		fmt.Printf("Updating from %s to %s\n", version, remoteVersion)
	}

	if !quiet {
		fmt.Println("Building giet...")
	}
	buildCmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", "../giet", "./cmd")
	buildCmd.Dir = modDir
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return false, fmt.Errorf("go build failed: %w", err)
	}

	newBinary := filepath.Join(repoDir, "giet")
	if _, err := os.Stat(newBinary); err != nil {
		return false, fmt.Errorf("build did not produce a binary")
	}

	if err := os.Rename(newBinary, exe); err != nil {
		if !quiet {
			fmt.Printf("os.Rename failed: %v, trying mv -f...\n", err)
		}
		cmd := exec.Command("mv", "-f", newBinary, exe)
		if out, err := cmd.CombinedOutput(); err != nil {
			return false, fmt.Errorf("failed to replace binary: %w (output: %s)", err, out)
		}
	}
	if err := os.Chmod(exe, 0755); err != nil {
		return false, fmt.Errorf("failed to set permissions: %w", err)
	}

	if !quiet {
		commit := getLatestCommitHash(repoDir)
		fmt.Printf("Updated to version %s (commit %s)\n", remoteVersion, commit)
	}
	return true, nil
}

func getLatestCommitHash(repoDir string) string {
	if _, err := exec.LookPath("git"); err == nil {
		cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
		cmd.Dir = repoDir
		out, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	headPath := filepath.Join(repoDir, ".git", "HEAD")
	if data, err := os.ReadFile(headPath); err == nil {
		line := strings.TrimSpace(string(data))
		if strings.HasPrefix(line, "ref: ") {
			ref := strings.TrimPrefix(line, "ref: ")
			refPath := filepath.Join(repoDir, ".git", ref)
			if data2, err := os.ReadFile(refPath); err == nil {
				return strings.TrimSpace(string(data2))[:7]
			}
		}
	}
	return "unknown"
}

func copyRecursive(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return copyFile(src, dst)
	}
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyRecursive(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
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
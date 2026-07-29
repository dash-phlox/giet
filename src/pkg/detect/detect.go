package detect

import (
    "bufio"
    "os"
    "os/exec"
    "runtime"
    "strings"
)

func System() (string, string) {
    distroID := getDistroID()
    arch := GetArch()
    return distroID, arch
}

func GetArch() string {
    arch := runtime.GOARCH
    switch arch {
    case "amd64":
        arch = "x86_64"
    case "arm64":
        arch = "aarch64"
    case "386":
        arch = "i686"
    case "arm":
        arch = "armv7l"
    }
    return arch
}

func getDistroID() string {
    file, err := os.Open("/etc/os-release")
    if err != nil {
        return "unknown"
    }
    defer file.Close()

    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := scanner.Text()
        if strings.HasPrefix(line, "ID=") {
            id := strings.TrimPrefix(line, "ID=")
            id = strings.Trim(id, `"'`)
            return normalizeDistroID(id)
        }
    }
    return "unknown"
}

func normalizeDistroID(id string) string {
    switch id {
    case "rhel", "centos", "rocky", "almalinux":
        return "rhel"
    case "opensuse-leap", "opensuse-tumbleweed":
        return "opensuse"
    default:
        return id
    }
}

func IsAndroid() bool {
    if _, err := os.Stat("/data/data/com.termux"); err == nil {
        return true
    }
    if os.Getenv("ANDROID_ROOT") != "" {
        return true
    }
    return false
}

func IsNixOS() bool {
    if _, err := os.Stat("/etc/NIXOS"); err == nil {
        return true
    }
    if os.Getenv("NIXOS") != "" {
        return true
    }
    return false
}

func checkIDLike(target string) bool {
    file, err := os.Open("/etc/os-release")
    if err != nil {
        return false
    }
    defer file.Close()
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := scanner.Text()
        if strings.HasPrefix(line, "ID_LIKE=") {
            val := strings.TrimPrefix(line, "ID_LIKE=")
            val = strings.Trim(val, `"'`)
            parts := strings.Split(val, " ")
            for _, p := range parts {
                if p == target {
                    return true
                }
            }
        }
    }
    return false
}

func GetDisplayName() string {
    file, err := os.Open("/etc/os-release")
    if err != nil {
        return "Unknown Linux"
    }
    defer file.Close()

    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := scanner.Text()
        if strings.HasPrefix(line, "NAME=") {
            name := strings.TrimPrefix(line, "NAME=")
            name = strings.Trim(name, `"`)
            if idx := strings.Index(name, "("); idx != -1 {
                name = strings.TrimSpace(name[:idx])
            }
            return name
        }
    }
    return "Unknown Linux"
}

func HasGLIBC() bool {
    cmd := exec.Command("getconf", "GNU_LIBC_VERSION")
    if err := cmd.Run(); err == nil {
        return true
    }
    cmd = exec.Command("ldd", "--version")
    out, err := cmd.Output()
    if err == nil && (strings.Contains(string(out), "glibc") || strings.Contains(string(out), "GNU C Library")) {
        return true
    }
    if _, err := os.Stat("/lib/libc.so.6"); err == nil {
        return true
    }
    if _, err := os.Stat("/lib64/libc.so.6"); err == nil {
        return true
    }
    return false
}

func IsArchFamily() bool {
    if _, err := os.Stat("/etc/arch-release"); err == nil {
        return true
    }
    id := getDistroID()
    if id == "arch" || id == "manjaro" || id == "cachyos" || id == "endeavouros" || id == "artix" || id == "garuda" {
        return true
    }
    if checkIDLike("arch") {
        return true
    }
    if _, err := exec.LookPath("pacman"); err == nil {
        return true
    }
    return false
}
package db

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "syscall"
    "time"
)

type PackageInfo struct {
    Owner         string    `json:"owner"`
    Repo          string    `json:"repo"`
    URL           string    `json:"url"`
    AssetURL      string    `json:"asset_url"`
    Version       string    `json:"version"`
    PackageName   string    `json:"package_name"`
    BinName       string    `json:"bin_name"`
    InstallTime   time.Time `json:"install_time"`
    LockedVersion string    `json:"locked_version"`
}

func getDBPath() string {
    homeDir, err := os.UserHomeDir()
    if err != nil {
        return "/var/lib/giet/installed.json"
    }
    return filepath.Join(homeDir, ".local", "var", "lib", "giet", "installed.json")
}

func getLockPath() string {
    return getDBPath() + ".lock"
}

func ensureDir() error {
    dbPath := getDBPath()
    dir := filepath.Dir(dbPath)
    return os.MkdirAll(dir, 0755)
}

var lockFile *os.File

func acquireLock() error {
    lockPath := getLockPath()
    f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0644)
    if err != nil {
        return err
    }
    if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
        f.Close()
        return err
    }
    lockFile = f
    return nil
}

func releaseLock() error {
    if lockFile == nil {
        return nil
    }
    if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
        return err
    }
    if err := lockFile.Close(); err != nil {
        return err
    }
    lockFile = nil
    return nil
}

func loadData() (map[string]PackageInfo, error) {
    dbPath := getDBPath()
    data := make(map[string]PackageInfo)
    if _, err := os.Stat(dbPath); os.IsNotExist(err) {
        return data, nil
    }
    f, err := os.Open(dbPath)
    if err != nil {
        return nil, err
    }
    defer f.Close()
    err = json.NewDecoder(f).Decode(&data)
    return data, err
}

func saveData(data map[string]PackageInfo) error {
    dbPath := getDBPath()
    f, err := os.Create(dbPath)
    if err != nil {
        return err
    }
    defer f.Close()
    return json.NewEncoder(f).Encode(data)
}

func EnsureDBDir() error {
    return ensureDir()
}

func Load() (map[string]PackageInfo, error) {
    if err := acquireLock(); err != nil {
        return nil, err
    }
    defer releaseLock()
    return loadData()
}

func Save(data map[string]PackageInfo) error {
    if err := acquireLock(); err != nil {
        return err
    }
    defer releaseLock()
    return saveData(data)
}

func AddOrUpdate(key string, info PackageInfo) error {
    if err := acquireLock(); err != nil {
        return err
    }
    defer releaseLock()
    data, err := loadData()
    if err != nil {
        return err
    }
    data[key] = info
    return saveData(data)
}

func Remove(key string) error {
    if err := acquireLock(); err != nil {
        return err
    }
    defer releaseLock()
    data, err := loadData()
    if err != nil {
        return err
    }
    if _, exists := data[key]; !exists {
        return fmt.Errorf("package not found in giet database: %s", key)
    }
    delete(data, key)
    return saveData(data)
}

func List() (map[string]PackageInfo, error) {
    return Load()
}
package db

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
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

func EnsureDBDir() error {
    dbPath := getDBPath()
    dir := filepath.Dir(dbPath)
    return os.MkdirAll(dir, 0755)
}

func Load() (map[string]PackageInfo, error) {
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

func Save(data map[string]PackageInfo) error {
    dbPath := getDBPath()
    f, err := os.Create(dbPath)
    if err != nil {
        return err
    }
    defer f.Close()
    return json.NewEncoder(f).Encode(data)
}

func AddOrUpdate(key string, info PackageInfo) error {
    if err := EnsureDBDir(); err != nil {
        return err
    }
    data, err := Load()
    if err != nil {
        return err
    }
    data[key] = info
    return Save(data)
}

func Remove(key string) error {
    data, err := Load()
    if err != nil {
        return err
    }
    if _, exists := data[key]; !exists {
        return fmt.Errorf("package not found in giet database: %s", key)
    }
    delete(data, key)
    return Save(data)
}

func List() (map[string]PackageInfo, error) {
    return Load()
}
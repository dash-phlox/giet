package github

import (
    "encoding/json"
    "fmt"
    "net/http"
    "net/url"
    "strings"
)

type GitHubRelease struct {
    TagName    string `json:"tag_name"`
    Prerelease bool   `json:"prerelease"`
    Assets     []struct {
        Name               string `json:"name"`
        BrowserDownloadURL string `json:"browser_download_url"`
    } `json:"assets"`
}

func ParseRepoURL(raw string) (owner, repo string, err error) {
    u, err := url.Parse(raw)
    if err != nil {
        return "", "", err
    }
    path := strings.TrimPrefix(u.Path, "/")
    parts := strings.Split(path, "/")
    if len(parts) < 2 {
        return "", "", fmt.Errorf("invalid GitHub URL: need owner/repo")
    }
    return parts[0], parts[1], nil
}

func GetLatestRelease(owner, repo string) (*GitHubRelease, error) {
    apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
    req, err := http.NewRequest("GET", apiURL, nil)
    if err != nil {
        return nil, err
    }
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusOK {
        var release GitHubRelease
        if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
            return nil, err
        }
        return &release, nil
    }

    if resp.StatusCode == http.StatusNotFound {
        tagsURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/tags", owner, repo)
        tagReq, _ := http.NewRequest("GET", tagsURL, nil)
        tagResp, err := client.Do(tagReq)
        if err != nil {
            return nil, err
        }
        defer tagResp.Body.Close()

        if tagResp.StatusCode != http.StatusOK {
            return nil, fmt.Errorf("GitHub API returned %s for tags", tagResp.Status)
        }

        var tags []struct {
            Name string `json:"name"`
        }
        if err := json.NewDecoder(tagResp.Body).Decode(&tags); err != nil {
            return nil, err
        }

        if len(tags) == 0 {
            fmt.Printf("No releases or tags found for %s/%s, falling back to default branch (HEAD)\n", owner, repo)
            return &GitHubRelease{
                TagName: "HEAD",
                Assets:  []struct {
                    Name               string `json:"name"`
                    BrowserDownloadURL string `json:"browser_download_url"`
                }{},
            }, nil
        }

        fmt.Printf("No releases found, falling back to latest tag: %s\n", tags[0].Name)
        return &GitHubRelease{
            TagName: tags[0].Name,
            Assets:  []struct {
                Name               string `json:"name"`
                BrowserDownloadURL string `json:"browser_download_url"`
            }{},
        }, nil
    }

    return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
}

func GetAllReleases(owner, repo string) ([]GitHubRelease, error) {
    var allReleases []GitHubRelease
    page := 1
    for {
        apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?page=%d&per_page=100", owner, repo, page)
        req, err := http.NewRequest("GET", apiURL, nil)
        if err != nil {
            return nil, err
        }
        client := &http.Client{}
        resp, err := client.Do(req)
        if err != nil {
            return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
            return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
        }
        var releases []GitHubRelease
        if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
            return nil, err
        }
        if len(releases) == 0 {
            break
        }
        allReleases = append(allReleases, releases...)
        if len(releases) < 100 {
            break
        }
        page++
    }
    return allReleases, nil
}

func FindFirstReleaseWithCompatibleAsset(owner, repo, arch string) (*GitHubRelease, string, error) {
    releases, err := GetAllReleases(owner, repo)
    if err != nil {
        return nil, "", err
    }
    for _, release := range releases {
        if release.TagName == "" {
            continue
        }
        assetURL := findCompatibleAssetInRelease(&release, arch, true)
        if assetURL != "" {
            return &release, assetURL, nil
        }
        assetURL = findCompatibleAssetInRelease(&release, arch, false)
        if assetURL != "" {
            return &release, assetURL, nil
        }
    }
    return nil, "", nil
}

func findCompatibleAssetInRelease(release *GitHubRelease, arch string, strict bool) string {
    matchArch := arch
    if arch == "aarch64" {
        matchArch = "arm64"
    } else if arch == "x86_64" {
        matchArch = "x64"
    }

    for _, asset := range release.Assets {
        name := asset.Name
        lowerName := strings.ToLower(name)

        if strings.Contains(lowerName, "darwin") || strings.Contains(lowerName, "macos") ||
            strings.Contains(lowerName, "windows") || strings.Contains(lowerName, "win32") || strings.Contains(lowerName, "win") {
            continue
        }

        isSupported := strings.HasSuffix(lowerName, ".appimage") ||
            strings.HasSuffix(lowerName, ".tar.gz") ||
            strings.HasSuffix(lowerName, ".tgz") ||
            strings.HasSuffix(lowerName, ".tar.xz") ||
            strings.HasSuffix(lowerName, ".zip")
        if !isSupported {
            continue
        }

        archMatch := strings.Contains(lowerName, arch) ||
            strings.Contains(lowerName, matchArch) ||
            (arch == "x86_64" && strings.Contains(lowerName, "amd64"))

        if strict && !archMatch {
            continue
        }
        if !strict {
            if !strings.Contains(lowerName, "linux") {
                continue
            }
            return asset.BrowserDownloadURL
        }
        if archMatch {
            return asset.BrowserDownloadURL
        }
    }
    return ""
}

func GetRepoInfo(owner, repo string) (string, error) {
    apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
    req, err := http.NewRequest("GET", apiURL, nil)
    if err != nil {
        return "", err
    }
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("GitHub API returned %s", resp.Status)
    }
    var repoInfo struct {
        Description string `json:"description"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&repoInfo); err != nil {
        return "", err
    }
    return repoInfo.Description, nil
}

func GetReleaseByTag(owner, repo, tag string) (*GitHubRelease, error) {
    apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", owner, repo, tag)
    req, err := http.NewRequest("GET", apiURL, nil)
    if err != nil {
        return nil, err
    }
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
    }
    var release GitHubRelease
    if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
        return nil, err
    }
    return &release, nil
}
# Giet

**Giet** is a GitHub‑based package manager for Linux.  
It installs pre‑built binaries directly from GitHub releases, supports local files (tarballs, zips, AppImages, RPMs), and offers basic package management (install, remove, update, lock/unlock).

## Features

- Install packages from GitHub URLs or `owner/repo` strings
- Install local packages (`.tar.gz`, `.tgz`, `.tar.xz`, `.tar`, `.zip`, `.rpm`, `.appimage`)
- List installed packages
- Remove packages
- Update single, multiple, or all packages
- Lock packages to a specific version
- Unlock packages

## Installation

### From Source

The only way to install Giet currently is to compile it from source.  
You need [Go](https://go.dev/doc/install) and [Git](https://git-scm.com/install/linux).

```sh
git clone https://github.com/dash-phlox/giet.git
cd giet/src
go mod init giet
go mod tidy
go build -o giet ./cmd
sudo mv giet /usr/local/bin/
```

> You can replace `sudo` with `doas` if you prefer.

## Usage

```sh
giet -h
```

### Install a package from GitHub

```sh
giet -i owner/repo
# or
giet -i https://github.com/owner/repo
```

### Install a local package

```sh
giet -i /path/to/package.tar.gz
```

### List installed packages

```sh
giet -ls
```

### Remove a package

```sh
giet -r package-name
```

### Update a package

```sh
giet -u package-name
# update all packages
giet -u
```

### Lock a package to its current version

```sh
giet --lock package-name
```

### Unlock a package

```sh
giet --unlock package-name
```

### Show version

```sh
giet -v
```

## Supported Formats

- **GitHub releases**: `.rpm`, `.appimage`, `.tar.gz`, `.tgz`, `.tar.xz`, `.zip`
- **Local files**: same as above, plus plain `.tar`

## Contributing

Issues and pull requests are welcome.
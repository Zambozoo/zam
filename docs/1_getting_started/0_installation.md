# Installation
## Mac and Linux
Zam is available through homebrew.
```
brew tap zambozoo/tap
brew install zam
zam -h
```

## Windows
Zam is available through scoop.
```
scoop bucket add zambozoo https://github.com/Zambozoo/scoop-zam
scoop install zam
zam -h
```

## Binary
Zam can be built locally through this repository as well.

Make sure you have the following installed:
* `go`
* `task`
* `cc`

Then fetch and build.
```
git clone https://github.com/zambozoo/zam
task build
./zam -h
```
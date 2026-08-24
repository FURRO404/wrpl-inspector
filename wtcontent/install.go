package wtcontent

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/maxsupermanhd/wrpl-inspector/v3/wrpl"
)

// mapsPack is the texture pack that holds the minimap pictures.
const mapsPack = "locations_maps.dxp.bin"

// Install is a War Thunder installation directory. It reads vromfs images and
// texture packs on demand and keeps what it read.
type Install struct {
	Root string

	lock  sync.Mutex
	vroms map[string]*VROMFS
	names map[string][]string
	maps  *DXP
}

// OpenInstall opens the installation at root. It checks that the directory
// holds aces.vromfs.bin, so a wrong path fails here instead of later.
func OpenInstall(root string) (*Install, error) {
	if _, err := os.Stat(filepath.Join(root, "aces.vromfs.bin")); err != nil {
		return nil, fmt.Errorf("wtcontent: %q is not a War Thunder installation: %w", root, err)
	}
	return &Install{
		Root:  root,
		vroms: map[string]*VROMFS{},
		names: map[string][]string{},
	}, nil
}

// FindInstall looks for an installation. It takes WT_ROOT when that is set,
// and otherwise searches the usual Steam library directories.
func FindInstall() (*Install, error) {
	if root := os.Getenv("WT_ROOT"); root != "" {
		return OpenInstall(root)
	}
	for _, root := range steamGameDirs() {
		if in, err := OpenInstall(root); err == nil {
			return in, nil
		}
	}
	return nil, errors.New("wtcontent: no War Thunder installation found, set WT_ROOT")
}

// steamLibraryPath pulls the library directories out of libraryfolders.vdf.
var steamLibraryPath = regexp.MustCompile(`"path"\s+"([^"]+)"`)

// steamGameDirs lists the directories a War Thunder installation can sit in.
func steamGameDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	steam := []string{
		filepath.Join(home, ".local", "share", "Steam"),
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"),
	}
	// Every library directory in the index is a place the game can live.
	roots := append([]string(nil), steam...)
	for _, root := range steam {
		vdf, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
		if err != nil {
			continue
		}
		for _, m := range steamLibraryPath.FindAllStringSubmatch(string(vdf), -1) {
			roots = append(roots, strings.ReplaceAll(m[1], `\\`, `\`))
		}
	}
	dirs := make([]string, 0, len(roots))
	for _, root := range roots {
		dirs = append(dirs, filepath.Join(root, "steamapps", "common", "War Thunder"))
	}
	return dirs
}

// VROMFS reads one vromfs image out of the installation, such as
// "aces.vromfs.bin". The image stays in memory for later calls.
func (in *Install) VROMFS(img string) (*VROMFS, error) {
	v, _, err := in.vromfs(img)
	return v, err
}

// vromfs returns an image and its name map, reading them once.
func (in *Install) vromfs(img string) (*VROMFS, []string, error) {
	in.lock.Lock()
	defer in.lock.Unlock()
	if v, ok := in.vroms[img]; ok {
		return v, in.names[img], nil
	}
	raw, err := os.ReadFile(filepath.Join(in.Root, img))
	if err != nil {
		return nil, nil, err
	}
	v, err := ReadVROMFS(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", img, err)
	}
	// An image without a name map holds FAT BLKs, which carry their own
	// names, so a failure here is not fatal.
	names, _ := wrpl.ParseNameMap(v.Files[NameMapEntry])
	in.vroms[img] = v
	in.names[img] = names
	return v, names, nil
}

// Blk parses one BLK file out of a vromfs image, with the name map and the
// shared dictionary that image carries.
func (in *Install) Blk(img, path string) (map[string]any, error) {
	v, names, err := in.vromfs(img)
	if err != nil {
		return nil, err
	}
	blk, ok := v.Files[path]
	if !ok {
		return nil, fmt.Errorf("%s: no file %q", img, path)
	}
	m, err := wrpl.ParseBlkWithNameMapDict(blk, names, v.Dict())
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", img, path, err)
	}
	return m, nil
}

// Maps opens the texture pack that holds the tank map pictures.
func (in *Install) Maps() (*DXP, error) {
	in.lock.Lock()
	defer in.lock.Unlock()
	if in.maps != nil {
		return in.maps, nil
	}
	found, err := filepath.Glob(filepath.Join(in.Root, "content*", "*", "res", mapsPack))
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("wtcontent: no %s under %q", mapsPack, in.Root)
	}
	raw, err := os.ReadFile(found[0])
	if err != nil {
		return nil, err
	}
	pack, err := ReadDXP(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", found[0], err)
	}
	in.maps = pack
	return pack, nil
}

// LevelName turns a level path such as "levels/avg_poland.bin" into the bare
// name the rest of the game data uses.
func LevelName(level string) string {
	return strings.TrimSuffix(strings.TrimPrefix(level, "levels/"), ".bin")
}

// MapKind selects one of the minimap pictures a level can have. Its value is
// the suffix the picture carries in the pack. A level can hold either kind or
// both: 145 levels have an air picture and 85 have a ground picture, and 75 of
// those hold both.
type MapKind string

const (
	// MapGround is the tank minimap. It covers the ground battle area only.
	MapGround MapKind = "_tankmap"
	// MapAir is the wider minimap that air and naval battles use.
	MapAir MapKind = "_map"
)

// LevelMap returns a minimap picture of one level. Pass either a level path or
// a bare level name. It tries each kind in the order given and returns the
// first one the pack holds, along with the kind it took. With no kind given it
// tries the ground map and then the air map.
func (in *Install) LevelMap(level string, kinds ...MapKind) (*image.RGBA, MapKind, error) {
	if len(kinds) == 0 {
		kinds = []MapKind{MapGround, MapAir}
	}
	pack, err := in.Maps()
	if err != nil {
		return nil, "", err
	}
	name := LevelName(level)
	for _, kind := range kinds {
		i := pack.Index(name + string(kind))
		if i < 0 {
			continue
		}
		data, err := pack.Payload(i)
		if err != nil {
			return nil, kind, err
		}
		img, err := pack.Header(i).Image(data)
		return img, kind, err
	}
	return nil, "", fmt.Errorf("%w: no %v picture for level %q", ErrDXPNotFound, kinds, name)
}

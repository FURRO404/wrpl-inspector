package wtcontent_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxsupermanhd/wrpl-inspector/v3/wrpl"
	"github.com/maxsupermanhd/wrpl-inspector/v3/wtcontent"
)

// wtRoot returns the War Thunder installation directory, or skips the test.
// The install is large, so it is not part of this repository:
//
//	WT_ROOT="$HOME/.steam/steam/steamapps/common/War Thunder" go test ./...
func wtRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("WT_ROOT")
	if root == "" {
		t.Skip("set WT_ROOT to a War Thunder installation directory")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("WT_ROOT is not readable: %v", err)
	}
	return root
}

// TestInstallVROMFS reads every vromfs image in the install.
func TestInstallVROMFS(t *testing.T) {
	root := wtRoot(t)
	images, err := filepath.Glob(filepath.Join(root, "*.vromfs.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(images) == 0 {
		t.Fatalf("no vromfs images in %q", root)
	}
	for _, p := range images {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		v, err := wtcontent.ReadVROMFS(raw)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		if len(v.Files) == 0 {
			t.Errorf("%s: no files", filepath.Base(p))
		}
	}
}

// TestInstallLevelBlk parses the level descriptions in aces.vromfs.bin. They
// are SLIM_ZSTD_DICT BLKs, so they need both the name map and the shared
// dictionary the image carries.
func TestInstallLevelBlk(t *testing.T) {
	root := wtRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "aces.vromfs.bin"))
	if err != nil {
		t.Skipf("aces.vromfs.bin: %v", err)
	}
	v, err := wtcontent.ReadVROMFS(raw)
	if err != nil {
		t.Fatal(err)
	}
	names, err := wrpl.ParseNameMap(v.Files[wtcontent.NameMapEntry])
	if err != nil {
		t.Fatal(err)
	}
	dict := v.Dict()
	if len(dict) == 0 {
		t.Fatal("aces.vromfs.bin has no shared dictionary")
	}

	levels, withCoords := 0, 0
	for name, blk := range v.Files {
		if filepath.Dir(name) != "levels" || filepath.Ext(name) != ".blk" {
			continue
		}
		levels++
		parsed, err := wrpl.ParseBlkWithNameMapDict(blk, names, dict)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if _, ok := parsed["tankMapCoord0"]; ok {
			withCoords++
		}
	}
	if levels == 0 {
		t.Fatal("no levels in aces.vromfs.bin")
	}
	// Ground maps carry tank map coordinates; air only maps do not.
	if withCoords == 0 {
		t.Errorf("none of the %d levels carry tankMapCoord0", levels)
	}
	t.Logf("parsed %d levels, %d with tank map coordinates", levels, withCoords)
}

// TestInstallLevelMaps decodes every minimap picture in the installation. A
// level can have a ground picture, an air picture, or both.
func TestInstallLevelMaps(t *testing.T) {
	root := wtRoot(t)
	in, err := wtcontent.OpenInstall(root)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := in.Maps()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[wtcontent.MapKind]int{}
	for _, name := range pack.Names() {
		base, _, _ := strings.Cut(name, "*")
		for _, kind := range []wtcontent.MapKind{wtcontent.MapGround, wtcontent.MapAir} {
			// The thumbnails end in the same text, so skip them here.
			if !strings.HasSuffix(base, string(kind)) {
				continue
			}
			level := strings.TrimSuffix(base, string(kind))
			img, got, err := in.LevelMap(level, kind)
			if err != nil {
				t.Errorf("%s: %v", base, err)
				break
			}
			if got != kind {
				t.Errorf("%s: kind %q, want %q", base, got, kind)
			}
			if img.Rect.Dx() < 256 || img.Rect.Dy() < 256 {
				t.Errorf("%s: size %v", base, img.Rect)
			}
			counts[kind]++
			break
		}
	}
	if counts[wtcontent.MapGround] == 0 || counts[wtcontent.MapAir] == 0 {
		t.Fatalf("decoded %d ground and %d air pictures", counts[wtcontent.MapGround], counts[wtcontent.MapAir])
	}
	t.Logf("decoded %d ground and %d air pictures", counts[wtcontent.MapGround], counts[wtcontent.MapAir])
}

// TestInstallDXP reads every texture pack in the install and converts a
// sample of the textures to DDS.
func TestInstallDXP(t *testing.T) {
	root := wtRoot(t)
	var packs []string
	for _, dir := range []string{"content", "content.hq"} {
		matches, err := filepath.Glob(filepath.Join(root, dir, "*", "res", "*.dxp.bin"))
		if err != nil {
			t.Fatal(err)
		}
		packs = append(packs, matches...)
	}
	if len(packs) == 0 {
		t.Skipf("no texture packs under %q", root)
	}

	textures, converted := 0, 0
	for _, p := range packs {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		pack, err := wtcontent.ReadDXP(raw)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		for i, name := range pack.Names() {
			textures++
			// Every pack holds thousands of textures, so sample them.
			if textures%23 != 0 || pack.Header(i).PackedSz == 0 {
				continue
			}
			_, err := pack.Texture(name)
			switch {
			case err == nil:
				converted++
			case errors.Is(err, wtcontent.ErrDDSxFormat):
				// A raw pixel format has no DDS FourCC. Payload still works.
			default:
				t.Errorf("%s %s: %v", filepath.Base(p), name, err)
			}
		}
	}
	if converted == 0 {
		t.Fatalf("no textures converted out of %d", textures)
	}
	t.Logf("%d packs, %d textures, %d sampled conversions", len(packs), textures, converted)
}

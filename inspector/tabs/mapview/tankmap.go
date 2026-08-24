package tabMapview

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/maxsupermanhd/wrpl-inspector/v3/wtcontent"
)

// levelToMinimap loads a minimap picture for a level and reports which kind it
// took. A level can have a ground picture, an air picture, or both, and each
// kind has its own coordinate pair, so a kind the level gives no coordinates
// for is skipped. Pass an empty want to take the ground picture when there is
// one and the air picture otherwise.
//
// A War Thunder installation is tried first. Without one the ground picture
// comes from a cache directory, and failing that from the WtMiniMapPictures
// repository. That fallback has no air pictures.
func levelToMinimap(inst *wtcontent.Install, tankmapsPath, level string, offsets levelDef, want wtcontent.MapKind) (*image.RGBA, wtcontent.MapKind, error) {
	kinds := []wtcontent.MapKind{wtcontent.MapGround, wtcontent.MapAir}
	if want != "" {
		kinds = []wtcontent.MapKind{want}
	}
	// Drop the kinds this level gives no coordinates for.
	kinds = slices.DeleteFunc(kinds, func(kind wtcontent.MapKind) bool {
		_, _, ok := offsets.coordsFor(kind)
		return !ok
	})
	if len(kinds) == 0 {
		return nil, "", fmt.Errorf("level %q describes no minimap coordinates", level)
	}

	var errs []error
	if inst != nil {
		for _, kind := range kinds {
			im, _, err := inst.LevelMap(level, kind)
			if err == nil {
				return im, kind, nil
			}
			errs = append(errs, err)
		}
	}
	// The download fallback holds ground pictures only.
	if slices.Contains(kinds, wtcontent.MapGround) {
		im, err := downloadedTankmap(tankmapsPath, level)
		if err == nil {
			return im, wtcontent.MapGround, nil
		}
		errs = append(errs, err)
	}
	return nil, "", errors.Join(errs...)
}

// downloadedTankmap loads the ground picture from the cache directory, and
// fetches it from the WtMiniMapPictures repository when the cache misses.
func downloadedTankmap(tankmapsPath, level string) (*image.RGBA, error) {
	fname := wtcontent.LevelName(level) + `_tankmap.png`
	p := filepath.Join(tankmapsPath, fname)
	f, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			tankmapUrl := `https://raw.githubusercontent.com/LivingTheDagor/WtMiniMapPictures/refs/heads/main/2048/` + fname
			ret, err := http.Get(tankmapUrl)
			if err != nil {
				return nil, fmt.Errorf("failed fetching non cached tankmap %q: %w", tankmapUrl, err)
			}
			if ret.StatusCode != 200 {
				return nil, fmt.Errorf("failed fetching non cached tankmap %q: %s", tankmapUrl, ret.Status)
			}
			defer ret.Body.Close()
			f, err = io.ReadAll(ret.Body)
			if err != nil {
				return nil, fmt.Errorf("failed reading non cached tankmap %q: %s", tankmapUrl, err)
			}
			err = os.MkdirAll(tankmapsPath, 0644)
			if err != nil {
				return nil, fmt.Errorf("can't create tankmap cache dir %q: %s", tankmapsPath, err)
			}
			err = os.WriteFile(p, f, 0644)
			if err != nil {
				return nil, fmt.Errorf("can't write tankmap to cache %q: %s", p, err)
			}
		} else {
			return nil, err
		}
	}
	im, err := png.Decode(bytes.NewReader(f))
	if err != nil {
		return nil, err
	}
	im2, ok := im.(*image.RGBA)
	if !ok {
		im2 = image.NewRGBA(im.Bounds())
		draw.Draw(im2, im2.Bounds(), im, im.Bounds().Min, draw.Src)
	}
	return im2, nil
}

package tabMapview

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/maxsupermanhd/wrpl-inspector/v3/wtcontent"
)

// tankmapSpace is the grid that draw areas and coordinate scales use. It is
// independent of the size of the map picture.
const tankmapSpace = 2048

type MissionDef struct {
	Areas   map[string]AreaDef `json:"areas"`
	Imports map[string]any     `json:"imports"`
}

// blkTo converts a parsed BLK into one of the definition structs. The BLK
// values match the JSON the datamine repository holds, so the struct tags fit
// both sources.
func blkTo(blk map[string]any, out any) error {
	b, err := json.Marshal(blk)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func missionLoad(inst *wtcontent.Install, dataminePath, missionPath string) (*MissionDef, error) {
	if missionPath == "" {
		return nil, nil
	}
	var mission *MissionDef
	if inst != nil {
		blk, err := inst.Blk("mis.vromfs.bin", strings.ToLower(missionPath))
		if err == nil {
			mission = &MissionDef{}
			if err := blkTo(blk, mission); err != nil {
				return nil, err
			}
		}
	}
	if mission == nil {
		missionBytes, err := os.ReadFile(filepath.Join(dataminePath, strings.ToLower(`mis.vromfs.bin_u/`+missionPath+"x")))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(missionBytes, &mission); err != nil {
			return nil, err
		}
	}
	if mission == nil {
		return nil, errors.New("json unmarshal nil")
	}
	if len(mission.Imports) == 0 {
		return mission, nil
	}
	if mission.Areas == nil {
		mission.Areas = map[string]AreaDef{}
	}
	imp, ok := mission.Imports["import_record"].(map[string]any)
	if ok {
		fp, ok := imp["file"].(string)
		if !ok {
			return nil, errors.New("import record has no file path")
		}
		impMis, err := missionLoad(inst, dataminePath, fp)
		if err != nil {
			return nil, fmt.Errorf("importing %q: %w", fp, err)
		}
		if impMis != nil && impMis.Areas != nil {
			maps.Insert(mission.Areas, maps.All(impMis.Areas))
		}
	} else {
		imports, ok := mission.Imports["import_record"].([]any)
		if !ok {
			return nil, errors.New("import record is not an object or an array")
		}
		for i, impAny := range imports {
			imp, ok = impAny.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("import record %d is not an object", i)
			}
			fp, ok := imp["file"].(string)
			if !ok {
				return nil, fmt.Errorf("import record %d has no file path", i)
			}
			impMis, err := missionLoad(inst, dataminePath, fp)
			if err != nil {
				return nil, fmt.Errorf("importing %q: %w", fp, err)
			}
			if impMis != nil && impMis.Areas != nil {
				maps.Insert(mission.Areas, maps.All(impMis.Areas))
			}
		}
	}
	return mission, nil
}

type levelDef struct {
	// Each minimap picture has its own coordinate pair. The ground picture
	// covers the tank battle area; the air picture covers the whole level.
	MapCoord0     []float64 `json:"mapCoord0"`
	MapCoord1     []float64 `json:"mapCoord1"`
	TankMapCoord0 []float64 `json:"tankMapCoord0"`
	TankMapCoord1 []float64 `json:"tankMapCoord1"`

	// Coord0 and Coord1 are the pair that matches the picture in use. Call
	// useMap to set them.
	Coord0 []float64 `json:"-"`
	Coord1 []float64 `json:"-"`
}

// coordsFor returns the coordinate pair a picture kind uses. It reports false
// when the level does not describe that kind.
func (l levelDef) coordsFor(kind wtcontent.MapKind) (c0, c1 []float64, ok bool) {
	c0, c1 = l.MapCoord0, l.MapCoord1
	if kind == wtcontent.MapGround {
		c0, c1 = l.TankMapCoord0, l.TankMapCoord1
	}
	if len(c0) < 2 || len(c1) < 2 {
		return nil, nil, false
	}
	return c0, c1, true
}

// useMap points Coord0 and Coord1 at the pair the given picture kind uses. It
// reports false when the level does not describe that kind.
func (l *levelDef) useMap(kind wtcontent.MapKind) bool {
	c0, c1, ok := l.coordsFor(kind)
	if !ok {
		return false
	}
	l.Coord0, l.Coord1 = c0, c1
	return true
}

func getLevelCoords(inst *wtcontent.Install, dataminePath, levelPath string) (*levelDef, error) {
	var level levelDef
	if inst != nil {
		blk, err := inst.Blk("aces.vromfs.bin", strings.TrimSuffix(levelPath, ".bin")+".blk")
		if err == nil {
			if err := blkTo(blk, &level); err != nil {
				return nil, fmt.Errorf("parsing level %q: %w", levelPath, err)
			}
			return &level, nil
		}
	}
	p := filepath.Join(dataminePath, `aces.vromfs.bin_u/`+strings.TrimSuffix(levelPath, ".bin")+".blkx")
	levelBytes, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(levelBytes, &level); err != nil {
		return nil, fmt.Errorf("parsing level %q: %w", p, err)
	}
	return &level, nil
}

type AreaDef struct {
	Type string      `json:"type"`
	TM   [][]float32 `json:"tm"`
}

func findCaps(areas map[string]AreaDef, bttlType string, difficulty byte) (map[int]AreaDef, error) {
	if areas == nil {
		return nil, errors.New("nil areas")
	}
	lastUnderscore := strings.LastIndex(bttlType, "_")
	if lastUnderscore == -1 {
		return nil, fmt.Errorf("underscore not found in %q", bttlType)
	}
	bttlType = strings.ToLower(bttlType[lastUnderscore:])
	bttlType = strings.Trim(bttlType, "_")
	difficultyStr := snailDifficultyToStr(difficulty)
	ret := map[int]AreaDef{}
	var found *AreaDef
	switch bttlType {
	case "bttl":
		found = findAreaInList(areas, []string{
			"bttl_t1_capture_area_" + difficultyStr,
			"bttl_t1_capture_area_" + "arcade",
		})
		if found != nil {
			ret[0] = *found
		}
		found = findAreaInList(areas, []string{
			"bttl_t2_capture_area_" + difficultyStr,
			"bttl_t2_capture_area_" + "arcade",
		})
		if found != nil {
			ret[1] = *found
		}
	case "dom":
		found = findAreaInList(areas, []string{
			"dom_capture_area_01_" + difficultyStr,
			"dom_capture_area_01_" + "arcade",
		})
		if found != nil {
			ret[0] = *found
		}
		found = findAreaInList(areas, []string{
			"dom_capture_area_02_" + difficultyStr,
			"dom_capture_area_02_" + "arcade",
		})
		if found != nil {
			ret[1] = *found
		}
		found = findAreaInList(areas, []string{
			"dom_capture_area_03_" + difficultyStr,
			"dom_capture_area_03_" + "arcade",
		})
		if found != nil {
			ret[2] = *found
		}
	case "conq1":
		found = findAreaInList(areas, []string{
			"conq_capture_area_01_" + difficultyStr,
			"conq_capture_area_01_" + "arcade",
		})
		if found != nil {
			ret[0] = *found
		}
	case "conq2":
		found = findAreaInList(areas, []string{
			"conq_capture_area_02_" + difficultyStr,
			"conq_capture_area_02_" + "arcade",
		})
		if found != nil {
			ret[0] = *found
		}
	case "conq3":
		found = findAreaInList(areas, []string{
			"conq_capture_area_03_" + difficultyStr,
			"conq_capture_area_03_" + "arcade",
		})
		if found != nil {
			ret[0] = *found
		}
	case "conq4":
		found = findAreaInList(areas, []string{
			"conq_capture_area_04_" + difficultyStr,
			"conq_capture_area_04_" + "arcade",
		})
		if found != nil {
			ret[0] = *found
		}
	}
	return ret, nil
}

func findAreaInList(areas map[string]AreaDef, search []string) *AreaDef {
	for _, k := range search {
		ret, ok := areas[k]
		if ok {
			return &ret
		}
	}
	return nil
}

func findMainBattleArea(areas map[string]AreaDef, bttlType string, difficulty byte) (*AreaDef, string, error) {
	if areas == nil {
		return nil, "", errors.New("findMainBattleArea: nil areas")
	}
	lastUnderscore := strings.LastIndex(bttlType, "_")
	if lastUnderscore == -1 {
		return nil, "", fmt.Errorf("underscore not found in %q", bttlType)
	}
	bttlType = strings.ToLower(bttlType[lastUnderscore:])
	bttlType = strings.Trim(bttlType, "_")
	bttlType = strings.TrimRight(bttlType, "01234")
	difficultyStr := ""
	switch difficulty >> 2 & 3 {
	case 2:
		difficultyStr = "hardcore"
	case 1:
		difficultyStr = "realistic"
	case 0:
		difficultyStr = "arcade"
	// case 186:
	// 	difficultyStr = "hardcore"
	// case 181:
	// 	difficultyStr = "realistic"
	// case 117:
	// 	difficultyStr = "realistic"
	// case 48:
	// 	difficultyStr = "arcade"
	default:
		return nil, "", fmt.Errorf("findMainBattleArea: unknown difficulty %d (%d)", difficulty, difficulty>>2&3)
	}
	tryNames := []string{
		bttlType + "_battle_area_" + difficultyStr,
		bttlType + "_battle_area_" + "arcade",
		"bttl" + "_battle_area_" + difficultyStr,
		"bttl" + "_battle_area_" + "arcade",
		"dom" + "_battle_area_" + difficultyStr,
		"dom" + "_battle_area_" + "arcade",
		"battle_area",
	}
	for _, k := range tryNames {
		ret, ok := areas[k]
		if ok {
			return &ret, k, nil
		}
	}
	as := []string{}
	for an := range areas {
		as = append(as, an)
	}
	return nil, "", fmt.Errorf("findMainBattleArea: main battle area (tried %v) not found (difficulty %q) (blk %q) (out of %+#v)", tryNames, difficultyStr, bttlType, as)
}

func calcDrawArea(offsets *levelDef, area *AreaDef, makeSquare bool) (x, z, w, h float64) {
	areaSizeX := math.Abs(offsets.Coord1[0] - offsets.Coord0[0])
	areaSizeZ := math.Abs(offsets.Coord1[1] - offsets.Coord0[1])
	coordScaleX := areaSizeX / tankmapSpace
	coordScaleZ := areaSizeZ / tankmapSpace
	x = float64(area.TM[3][0]) - offsets.Coord0[0]
	z = float64(area.TM[3][2]) - offsets.Coord0[1]
	x /= coordScaleX
	z /= coordScaleZ
	z = tankmapSpace - z
	w = float64(area.TM[0][0] + area.TM[0][2])
	h = float64(area.TM[2][0] + area.TM[2][2])
	w /= coordScaleX
	h /= coordScaleZ
	if area.Type == "Cylinder" {
		w *= 2
		h *= 2
	}
	if (w > 0 && h < 0) || (w < 0 && h > 0) {
		tmp := w
		w = h
		h = tmp
	}
	if makeSquare {
		s := max(math.Abs(w), math.Abs(h))
		if w < 0 {
			w = -s
		} else {
			w = s
		}
		if h < 0 {
			h = -s
		} else {
			h = s
		}
	}
	x = x - w/2
	z = z - h/2
	return
}

func snailDifficultyToStr(difficulty byte) string {
	return []string{"arcade", "realistic", "hardcore"}[(difficulty>>2)&3]
}

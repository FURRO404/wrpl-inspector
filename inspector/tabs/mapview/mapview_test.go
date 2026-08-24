package tabMapview

import (
	"os"
	"testing"

	"github.com/maxsupermanhd/wrpl-inspector/v3/wtcontent"
)

// install opens the War Thunder installation, or skips the test:
//
//	WT_ROOT="$HOME/.steam/steam/steamapps/common/War Thunder" go test ./...
func install(t *testing.T) *wtcontent.Install {
	t.Helper()
	if os.Getenv("WT_ROOT") == "" {
		t.Skip("set WT_ROOT to a War Thunder installation directory")
	}
	in, err := wtcontent.FindInstall()
	if err != nil {
		t.Skip(err)
	}
	return in
}

// TestGetLevelCoordsFromInstall reads the tank map coordinates of a level out
// of the installation instead of the datamine checkout.
func TestGetLevelCoordsFromInstall(t *testing.T) {
	in := install(t)
	level, err := getLevelCoords(in, "", "levels/avg_poland.bin")
	if err != nil {
		t.Fatal(err)
	}
	if len(level.TankMapCoord0) != 2 || len(level.TankMapCoord1) != 2 {
		t.Fatalf("ground coordinates: got %v and %v", level.TankMapCoord0, level.TankMapCoord1)
	}
	if len(level.MapCoord0) != 2 || len(level.MapCoord1) != 2 {
		t.Fatalf("air coordinates: got %v and %v", level.MapCoord0, level.MapCoord1)
	}
	if level.TankMapCoord0[0] == level.TankMapCoord1[0] {
		t.Errorf("the map has no width: %v to %v", level.TankMapCoord0, level.TankMapCoord1)
	}
}

// TestMissionLoadFromInstall reads a mission out of the installation and
// checks that its capture areas carry a transform.
func TestMissionLoadFromInstall(t *testing.T) {
	in := install(t)
	mission, err := missionLoad(in, "", "gamedata/missions/cta/tanks/poland/mainareas/template_poland_dom_arcade.blk")
	if err != nil {
		t.Fatal(err)
	}
	if len(mission.Areas) == 0 {
		t.Fatal("no areas in the mission")
	}
	area, ok := mission.Areas["dom_capture_area_01_arcade"]
	if !ok {
		t.Fatal("no dom_capture_area_01_arcade in the mission")
	}
	if area.Type == "" {
		t.Error("the area has no type")
	}
	if len(area.TM) != 4 {
		t.Fatalf("transform: got %d rows, want 4", len(area.TM))
	}
	if area.TM[3][0] == 0 && area.TM[3][2] == 0 {
		t.Error("the area sits at the origin, so the transform did not load")
	}
}

// TestLevelToMinimapFromInstall loads a minimap picture without touching the
// cache directory or the network. A ground level gives the ground picture; an
// air level has no ground picture, so it gives the air one.
func TestLevelToMinimapFromInstall(t *testing.T) {
	in := install(t)
	for _, tc := range []struct {
		level string
		want  wtcontent.MapKind
	}{
		{"levels/avg_poland.bin", wtcontent.MapGround},
		{"levels/air_afghan.bin", wtcontent.MapAir},
	} {
		t.Run(tc.level, func(t *testing.T) {
			offsets, err := getLevelCoords(in, "", tc.level)
			if err != nil {
				t.Fatal(err)
			}
			_, kind, err := levelToMinimap(in, "", tc.level, *offsets, "")
			if err != nil {
				t.Fatal(err)
			}
			if kind != tc.want {
				t.Errorf("kind: got %q, want %q", kind, tc.want)
			}
			if !offsets.useMap(kind) {
				t.Fatalf("no coordinates for %q", kind)
			}
			if offsets.Coord0[0] == offsets.Coord1[0] {
				t.Errorf("the map has no width: %v to %v", offsets.Coord0, offsets.Coord1)
			}
		})
	}
}

// TestLevelToMinimapPixels checks that a loaded picture holds an image.
func TestLevelToMinimapPixels(t *testing.T) {
	in := install(t)
	offsets, err := getLevelCoords(in, "", "levels/avg_poland.bin")
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := levelToMinimap(in, "", "levels/avg_poland.bin", *offsets, "")
	if err != nil {
		t.Fatal(err)
	}
	if img.Rect.Dx() < 256 || img.Rect.Dy() < 256 {
		t.Fatalf("size: got %v", img.Rect)
	}
	// A map picture is not one flat color.
	first := img.Pix[0:4]
	same := true
	for i := 0; i < len(img.Pix); i += 4 {
		if !bytesEqual4(img.Pix[i:i+4], first) {
			same = false
			break
		}
	}
	if same {
		t.Error("the picture is one flat color")
	}
}

func bytesEqual4(a, b []byte) bool {
	return a[0] == b[0] && a[1] == b[1] && a[2] == b[2] && a[3] == b[3]
}

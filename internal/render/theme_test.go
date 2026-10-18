package render

import (
	"math"
	"strings"
	"testing"
)

func TestHouseThemesMeetContrast(t *testing.T) {
	roles := []string{"text", "muted", "accent", "success", "warning", "error", "info"}
	for _, name := range []string{"house-dark", "house-light"} {
		theme, err := houseTheme(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, role := range roles {
			for _, ground := range []string{"bg", "surface"} {
				ratio := contrastRatio(theme[role], theme[ground])
				if ratio < 4.5 {
					t.Errorf("%s %s on %s is %.2f:1", name, role, ground, ratio)
				}
			}
		}
	}
}

func TestScorecardStructureHasNoColorLiteral(t *testing.T) {
	if strings.Contains(scorecardStructureCSS, "#") {
		t.Fatal("scorecard structure carries a color literal; colors belong in the house files")
	}
	css, err := scorecardCSS()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(css), "#d2a8ff") {
		t.Fatal("lavender survived in the generated scorecard CSS")
	}
	if !strings.Contains(css, ":root{--bg:#0f1419;") || !strings.Contains(css, `html[data-theme="light"]{--bg:#ffffff;`) {
		t.Fatal("the scorecard did not open dark and carry the light palette")
	}
}

func contrastRatio(foreground, background string) float64 {
	lighter := relativeLuminance(foreground)
	darker := relativeLuminance(background)
	if darker > lighter {
		lighter, darker = darker, lighter
	}
	return (lighter + 0.05) / (darker + 0.05)
}

func relativeLuminance(color string) float64 {
	r := channel(color[1:3])
	g := channel(color[3:5])
	b := channel(color[5:7])
	return 0.2126*r + 0.7152*g + 0.0722*b
}

func channel(hex string) float64 {
	value := float64(hexByte(hex[0])<<4 | hexByte(hex[1]))
	value /= 255
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

func hexByte(r byte) byte {
	switch {
	case r >= '0' && r <= '9':
		return r - '0'
	default:
		return r - 'a' + 10
	}
}

package sun

import (
	"errors"
	"math"
	"testing"
)

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.3f, want %.3f ± %.3f", what, got, want, tol)
	}
}

// FAO-56's worked examples 8 to 10 (Allen et al. 1998, chapter 3).
func TestFAO56Examples(t *testing.T) {
	// Example 8: Ra on 3 September at 20° S.
	near(t, "Ra(-20°, 3 Sep)", Extraterrestrial(-20, 246), 32.2, 0.1)
	// Example 9: daylight hours on the same day.
	near(t, "N(-20°, 3 Sep)", DayLength(-20, 246), 11.7, 0.05)
	// Example 10: Rio de Janeiro (22°54' S) in May (its 15th), 7.1 h of
	// sun a day give Rs 14.5 MJ/m²: the hours back from the light.
	near(t, "N(Rio, May)", DayLength(-22.9, 135), 10.9, 0.05)
	near(t, "Ra(Rio, May)", Extraterrestrial(-22.9, 135), 25.1, 0.1)
	near(t, "n(Rio, May, 14.5 MJ)", BrightHours(-22.9, 135, 14.5), 7.1, 0.1)
}

func TestDayLength(t *testing.T) {
	for _, doy := range []int{1, 80, 172, 266, 355} {
		near(t, "equator", DayLength(0, doy), 12, 0.01)
	}
	near(t, "Hyderabad, June solstice", DayLength(17.4, 172), 13.0, 0.1)
	near(t, "Hyderabad, December solstice", DayLength(17.4, 355), 11.0, 0.1)
	near(t, "Delhi, June solstice", DayLength(28.6, 172), 13.8, 0.1)
	if got := DayLength(80, 172); got != 24 {
		t.Errorf("polar day = %v, want 24", got)
	}
	if got := DayLength(80, 355); got != 0 {
		t.Errorf("polar night = %v, want 0", got)
	}
}

// South of the Tropic of Cancer the noon sun is in the north for part of
// the year: at Hyderabad (17.4° N) from early May to early August.
func TestNoonSunSide(t *testing.T) {
	cases := []struct {
		lat     float64
		doy     int
		azimuth float64
		elev    float64
	}{
		{17.4, 80, 180, 72.6},              // March equinox: south, 90 - lat
		{17.4, 172, 0, 90 - (23.4 - 17.4)}, // June solstice: north of overhead
		{17.4, 355, 180, 90 - 17.4 - 23.4},
		{28.6, 172, 180, 90 - 28.6 + 23.4}, // Delhi: always south
		{-20, 172, 0, 90 - 20 - 23.4},      // south of the equator: north
	}
	for _, c := range cases {
		p := At(c.lat, c.doy, 12)
		near(t, "azimuth", p.Azimuth, c.azimuth, 0.5)
		near(t, "elevation", p.Elevation, c.elev, 0.6)
	}
}

func TestIncidence(t *testing.T) {
	flat := Slope{}
	// On flat ground the noon incidence is the sun's angle from overhead.
	near(t, "flat", Incidence(17.4, 355, 12, flat), 90-At(17.4, 355, 12).Elevation, 1e-6)
	// A slope facing the noon sun at its own elevation's complement takes
	// it straight on.
	p := At(28.6, 355, 12)
	s := Slope{TiltDeg: 90 - p.Elevation, AspectDeg: 180}
	near(t, "facing the sun", Incidence(28.6, 355, 12, s), 0, 0.01)
	// Facing away by the same angle doubles the flat ground's angle.
	s.AspectDeg = 0
	near(t, "facing away", Incidence(28.6, 355, 12, s), 2*(90-p.Elevation), 0.01)
}

// BeamRatio for a slope facing the equator matches the closed form for an
// equator-facing surface (Duffie and Beckman, eq. 2.19.3).
func TestBeamRatioClosedForm(t *testing.T) {
	for _, c := range []struct {
		lat, tilt float64
		doy       int
	}{{28.6, 10, 355}, {28.6, 25, 172}, {17.4, 15, 172}, {17.4, 15, 355}, {40, 30, 80}} {
		φ, β, δ := rad(c.lat), rad(c.tilt), rad(Declination(c.doy))
		ωs := sunsetAngle(c.lat, c.doy)
		ωs2 := math.Min(ωs, math.Acos(max(-1, min(1, -math.Tan(φ-β)*math.Tan(δ)))))
		want := (math.Cos(φ-β)*math.Cos(δ)*math.Sin(ωs2) + ωs2*math.Sin(φ-β)*math.Sin(δ)) /
			(math.Cos(φ)*math.Cos(δ)*math.Sin(ωs) + ωs*math.Sin(φ)*math.Sin(δ))
		got := BeamRatio(c.lat, c.doy, Slope{TiltDeg: c.tilt, AspectDeg: 180})
		near(t, "Rb", got, want, 0.005)
	}
}

func TestSlopeLight(t *testing.T) {
	if got := BeamRatio(20, 100, Slope{}); math.Abs(got-1) > 1e-9 {
		t.Errorf("flat Rb = %v, want 1", got)
	}
	if got := OnSlope(20, 4, 22, Slope{}); got != 22 {
		t.Errorf("flat OnSlope = %v, want 22", got)
	}
	// In a Delhi December a north-facing slope gets less, a south-facing
	// one more; east and west alike.
	north := OnSlope(28.6, 12, 14, FromPercent(20, 0))
	south := OnSlope(28.6, 12, 14, FromPercent(20, 180))
	east := OnSlope(28.6, 12, 14, FromPercent(20, 90))
	west := OnSlope(28.6, 12, 14, FromPercent(20, 270))
	if !(north < 14*0.8 && south > 14*1.15) {
		t.Errorf("Delhi December, 20%% slope: north %.1f, south %.1f MJ; flat 14", north, south)
	}
	near(t, "east vs west", east, west, 0.01)
	if east >= 14 {
		t.Errorf("east-facing slope %.2f MJ, want a little under flat 14", east)
	}
	// In a Hyderabad June the sun is overhead to the north: a gentle
	// north slope loses almost nothing.
	jun := OnSlope(17.4, 6, 19.7, FromPercent(5, 0))
	if jun < 19.7*0.98 {
		t.Errorf("Hyderabad June, 5%% north slope: %.2f MJ, want ≥ %.2f", jun, 19.7*0.98)
	}
}

func TestSlopeCheck(t *testing.T) {
	for _, s := range []Slope{{TiltDeg: -1}, {TiltDeg: 61}, {AspectDeg: 360}, {TiltDeg: math.NaN()}} {
		if err := s.Check(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: err %v, want ErrInvalid", s, err)
		}
	}
	if err := FromPercent(12, -90).Check(); err != nil {
		t.Errorf("FromPercent(12, -90): %v", err)
	}
	near(t, "100% tilt", FromPercent(100, 0).TiltDeg, 45, 1e-9)
}

func TestYear(t *testing.T) {
	// NASA POWER's normals at Hyderabad: the monsoon's cloud.
	flat := [12]float64{17.6, 20.7, 22.5, 24.0, 24.1, 19.7, 16.7, 16.5, 17.6, 18.2, 17.5, 16.8}
	clear := [12]float64{18.6, 21.6, 24.0, 25.9, 26.4, 25.2, 24.9, 24.5, 23.3, 21.4, 19.3, 17.9}
	y := Year(17.4, flat, clear, Slope{})
	jul := y[6]
	if jul.CloudLoss < 0.3 || jul.SunHours > 7 {
		t.Errorf("July: cloud loss %.2f, sun hours %.1f; want a cloudy month", jul.CloudLoss, jul.SunHours)
	}
	if y[0].SunHours < 8 {
		t.Errorf("January: %.1f sun hours, want a sunny month", y[0].SunHours)
	}
	if y[5].Noon.Azimuth != 0 || y[11].Noon.Azimuth != 180 {
		t.Errorf("noon sun June %.0f°, December %.0f°: want north then south", y[5].Noon.Azimuth, y[11].Noon.Azimuth)
	}
	for _, m := range y {
		if m.SlopeFactor != 1 || m.SlopeMJ != m.FlatMJ {
			t.Errorf("flat month %d: factor %v", m.Month, m.SlopeFactor)
		}
	}
	// Without light, the geometry stays.
	none := Year(17.4, [12]float64{}, [12]float64{}, Slope{})
	if none[0].FlatMJ != 0 || none[0].SunHours != 0 || none[0].DayLengthH == 0 {
		t.Errorf("no light: %+v", none[0])
	}
}

// North of the Tropic of Cancer shadows fall north all year; at 10° N the
// monsoon's shadows fall south, the winter's north; south of the equator
// the other way round.
func TestShadowNorth(t *testing.T) {
	if s := ShadowNorth(28.6, 152, 120); s < 0.1 {
		t.Errorf("Delhi, June to September: %.2f, want north", s)
	}
	if s := ShadowNorth(10, 152, 60); s > -0.1 {
		t.Errorf("Kochi, June and July: %.2f, want south", s)
	}
	if s := ShadowNorth(10, 305, 120); s < 0.3 {
		t.Errorf("Kochi, November to February: %.2f, want well north", s)
	}
	if s := ShadowNorth(-20, 152, 90); s > -0.3 {
		t.Errorf("20° S in June: %.2f, want well south", s)
	}
	// A whole year at the equator: about even.
	if s := ShadowNorth(0, 1, 365); s < -0.05 || s > 0.05 {
		t.Errorf("equator, a year: %.2f, want about 0", s)
	}
}

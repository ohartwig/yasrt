// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package semver

import "testing"

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    Version
		wantErr bool
	}{
		{in: "1.2.3", want: Version{1, 2, 3, ""}},
		{in: "0.0.0", want: Version{0, 0, 0, ""}},
		{in: "10.20.30", want: Version{10, 20, 30, ""}},
		{in: "1.2.3-rc.1", want: Version{1, 2, 3, "rc.1"}},
		{in: "v1.2.3", wantErr: true},
		{in: "1.2", wantErr: true},
		{in: "1.2.3.4", wantErr: true},
		{in: "01.2.3", wantErr: true},
		{in: "1.2.x", wantErr: true},
		{in: "", wantErr: true},
	} {
		got, err := Parse(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Parse(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
		if got.String() != tc.in {
			t.Errorf("round trip: %q -> %q", tc.in, got.String())
		}
	}
}

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.3", "1.2.4", -1},
		{"1.3.0", "1.2.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.2.3-rc.1", "1.2.3", -1}, // prerelease sorts before its release
		{"1.2.3", "1.2.3-rc.1", 1},
		{"1.2.3-rc.1", "1.2.3-rc.2", -1},
		{"1.2.3-rc.2", "1.2.3-rc.10", -1}, // numeric, not lexical
		{"1.2.3-alpha", "1.2.3-beta", -1},
		{"1.2.3-rc.1", "1.2.3-rc.1.1", -1},
	} {
		a, err := Parse(tc.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Parse(tc.b)
		if err != nil {
			t.Fatal(err)
		}
		if got := a.Compare(b); got != tc.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestNext(t *testing.T) {
	for _, tc := range []struct {
		name        string
		in          string
		bump        Bump
		majorOnZero bool
		want        string
	}{
		{"patch", "1.2.3", Patch, true, "1.2.4"},
		{"minor resets patch", "1.2.3", Minor, true, "1.3.0"},
		{"major resets both", "1.2.3", Major, true, "2.0.0"},
		{"none", "1.2.3", None, true, "1.2.3"},

		// major_on_zero: the whole point of the flag.
		{"0.x major allowed", "0.4.2", Major, true, "1.0.0"},
		{"0.x major lowered", "0.4.2", Major, false, "0.5.0"},
		{"0.x minor unaffected", "0.4.2", Minor, false, "0.5.0"},
		{"0.x patch unaffected", "0.4.2", Patch, false, "0.4.3"},
		{"1.x major unaffected by flag", "1.4.2", Major, false, "2.0.0"},

		// A prerelease patched becomes the release it prefixes.
		{"rc to release", "1.2.0-rc.1", Patch, true, "1.2.0"},
		{"rc to minor", "1.2.0-rc.1", Minor, true, "1.3.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := Parse(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := v.Next(tc.bump, tc.majorOnZero).String(); got != tc.want {
				t.Errorf("%s.Next(%v, majorOnZero=%v) = %s, want %s",
					tc.in, tc.bump, tc.majorOnZero, got, tc.want)
			}
		})
	}
}

func TestParseBumpAndMax(t *testing.T) {
	for in, want := range map[string]Bump{
		"major": Major, "MINOR": Minor, " patch ": Patch, "": None, "none": None,
	} {
		got, err := ParseBump(in)
		if err != nil {
			t.Errorf("ParseBump(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseBump(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseBump("huge"); err == nil {
		t.Error("ParseBump(huge) should fail")
	}
	if got := Max(Patch, Major); got != Major {
		t.Errorf("Max(patch,major) = %v", got)
	}
	if got := Max(Minor, None); got != Minor {
		t.Errorf("Max(minor,none) = %v", got)
	}
}

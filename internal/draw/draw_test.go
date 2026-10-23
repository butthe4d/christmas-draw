package draw

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mrand "math/rand/v2"
)

func TestAllowed(t *testing.T) {
	names := []string{"A", "B", "C", "D"}
	couples := [][2]string{{"A", "B"}}
	excl := [][2]string{{"C", "D"}}

	r := NewRules(names, couples, excl, true)
	if r.Allowed("A", "A") {
		t.Error("self must be forbidden")
	}
	if r.Allowed("A", "B") || r.Allowed("B", "A") {
		t.Error("couple must be forbidden both ways when excludeCouples")
	}
	if r.Allowed("C", "D") || r.Allowed("D", "C") {
		t.Error("exclusion must be forbidden both ways")
	}
	if !r.Allowed("A", "C") {
		t.Error("A->C should be allowed")
	}

	r = NewRules(names, couples, excl, false)
	if !r.Allowed("A", "B") || !r.Allowed("B", "A") {
		t.Error("couple must be allowed when not excludeCouples")
	}
	if r.Allowed("C", "D") || r.Allowed("D", "C") {
		t.Error("exclusion must hold even with excludeCouples=false")
	}
}

func TestFeasibleExplains(t *testing.T) {
	r := NewRules([]string{"A", "B", "C"}, [][2]string{{"A", "B"}}, nil, true)
	err := Feasible(r)
	var ie *InfeasibleError
	if !errors.As(err, &ie) {
		t.Fatalf("want *InfeasibleError, got %v", err)
	}
	if fmt.Sprint(ie.Givers) != "[A B]" || fmt.Sprint(ie.Receivers) != "[C]" {
		t.Errorf("got givers=%v receivers=%v", ie.Givers, ie.Receivers)
	}
	want := "A, B can only draw from C — not enough people"
	if ie.Error() != want {
		t.Errorf("Error() = %q, want %q", ie.Error(), want)
	}

	r = NewRules([]string{"A", "B", "C", "D"}, [][2]string{{"A", "B"}, {"C", "D"}}, nil, true)
	if err := Feasible(r); err != nil {
		t.Errorf("two couples should be feasible: %v", err)
	}

	r = NewRules([]string{"A", "B", "C"}, nil, [][2]string{{"A", "B"}, {"A", "C"}}, true)
	err = Feasible(r)
	if !errors.As(err, &ie) {
		t.Fatalf("want *InfeasibleError, got %v", err)
	}
	if len(ie.Receivers) != 0 {
		t.Errorf("receivers should be empty, got %v", ie.Receivers)
	}
	if !strings.Contains(ie.Error(), "A has nobody") {
		t.Errorf("message %q lacks 'A has nobody'", ie.Error())
	}
}

// bruteFeasible reports whether any valid assignment exists.
func bruteFeasible(r Rules) bool {
	names := r.Names()
	used := make([]bool, len(names))
	var rec func(i int) bool
	rec = func(i int) bool {
		if i == len(names) {
			return true
		}
		for j := range names {
			if !used[j] && r.Allowed(names[i], names[j]) {
				used[j] = true
				if rec(i + 1) {
					used[j] = false
					return true
				}
				used[j] = false
			}
		}
		return false
	}
	return rec(0)
}

func TestDrawInvariantsProperty(t *testing.T) {
	rng := mrand.New(mrand.NewPCG(1, 2))
	for iter := 0; iter < 2000; iter++ {
		n := 3 + rng.IntN(13)
		names := make([]string, n)
		for i := range names {
			names[i] = fmt.Sprintf("P%02d", i)
		}
		var couples, excl [][2]string
		for k := rng.IntN(n/2 + 1); k > 0; k-- {
			a, b := rng.IntN(n), rng.IntN(n)
			if a != b {
				couples = append(couples, [2]string{names[a], names[b]})
			}
		}
		for k := rng.IntN(n + 1); k > 0; k-- {
			a, b := rng.IntN(n), rng.IntN(n)
			if a != b {
				excl = append(excl, [2]string{names[a], names[b]})
			}
		}
		r := NewRules(names, couples, excl, rng.IntN(2) == 0)

		ferr := Feasible(r)
		if n <= 7 {
			if brute := bruteFeasible(r); brute != (ferr == nil) {
				t.Fatalf("iter %d: brute=%v Feasible=%v", iter, brute, ferr)
			}
		}
		a, derr := Draw(r, rand.Reader)
		if ferr == nil {
			if derr != nil {
				t.Fatalf("iter %d: Draw failed: %v", iter, derr)
			}
			if err := Check(r, a); err != nil {
				t.Fatalf("iter %d: Check: %v", iter, err)
			}
		} else {
			var ie *InfeasibleError
			if !errors.As(derr, &ie) {
				t.Fatalf("iter %d: want InfeasibleError, got %v", iter, derr)
			}
			if len(ie.Receivers) >= len(ie.Givers) && len(ie.Receivers) != 0 {
				t.Fatalf("iter %d: not a Hall violator: %v -> %v", iter, ie.Givers, ie.Receivers)
			}
		}
	}
}

func TestDrawUniformSmall(t *testing.T) {
	r := NewRules([]string{"A", "B", "C"}, nil, nil, true)
	counts := map[string]int{}
	for i := 0; i < 6000; i++ {
		a, err := Draw(r, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		counts[fmt.Sprintf("%s%s%s", a["A"], a["B"], a["C"])]++
	}
	if len(counts) != 2 {
		t.Fatalf("want 2 distinct assignments, got %v", counts)
	}
	for k, c := range counts {
		if c < 2700 || c > 3300 {
			t.Errorf("assignment %s drawn %d times", k, c)
		}
	}
}

func TestDrawBarelyFeasible(t *testing.T) {
	// Rules are symmetric, so "exactly one allowed receiver per giver" means
	// three disjoint allowed pairs; the only valid draw swaps within each pair.
	names := []string{"A", "B", "C", "D", "E", "F"}
	allowed := map[string]string{"A": "B", "B": "A", "C": "D", "D": "C", "E": "F", "F": "E"}
	var excl [][2]string
	for i, g := range names {
		for _, x := range names[i+1:] {
			if allowed[g] != x {
				excl = append(excl, [2]string{g, x})
			}
		}
	}
	r := NewRules(names, nil, excl, true)
	start := time.Now()
	a, err := Draw(r, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v", d)
	}
	for g, want := range allowed {
		if a[g] != want {
			t.Errorf("%s -> %s, want %s", g, a[g], want)
		}
	}
}

func TestCheck(t *testing.T) {
	r := NewRules([]string{"A", "B", "C", "D"}, nil, [][2]string{{"A", "B"}}, true)
	good := Assignment{"A": "C", "B": "D", "C": "A", "D": "B"}
	if err := Check(r, good); err != nil {
		t.Fatalf("good assignment rejected: %v", err)
	}
	cases := map[string]Assignment{
		"self":      {"A": "A", "B": "D", "C": "B", "D": "C"},
		"forbidden": {"A": "B", "B": "C", "C": "D", "D": "A"},
		"twice":     {"A": "C", "B": "C", "C": "A", "D": "B"},
		"missing":   {"A": "C", "B": "D", "C": "A"},
		"unknown":   {"A": "C", "B": "D", "C": "A", "D": "Z"},
		"unknownG":  {"A": "C", "B": "D", "C": "A", "D": "B", "Z": "A"},
	}
	for name, a := range cases {
		if err := Check(r, a); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// Package draw decides who gives to whom: exclusion rules, a feasibility
// check with a plain explanation, a crypto-random draw and an invariant check.
package draw

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// maxShuffleAttempts is how many random permutations are tried before the
// draw falls back to randomized backtracking. A variable so tests can force
// the fallback.
var maxShuffleAttempts = 200000

// Rules says which giver may draw which receiver.
type Rules struct {
	names     []string
	forbidden map[[2]string]bool
}

// NewRules builds rules for names. Exclusions are always forbidden (both
// directions); couples are forbidden only when excludeCouples is set.
// Nobody may draw themselves.
func NewRules(names []string, couples, exclusions [][2]string, excludeCouples bool) Rules {
	r := Rules{
		names:     append([]string(nil), names...),
		forbidden: map[[2]string]bool{},
	}
	add := func(pairs [][2]string) {
		for _, p := range pairs {
			r.forbidden[[2]string{p[0], p[1]}] = true
			r.forbidden[[2]string{p[1], p[0]}] = true
		}
	}
	if excludeCouples {
		add(couples)
	}
	add(exclusions)
	return r
}

// Names returns a copy of the participant names, in the order given.
func (r Rules) Names() []string { return append([]string(nil), r.names...) }

// Allowed reports whether giver may draw receiver.
func (r Rules) Allowed(giver, receiver string) bool {
	return giver != receiver && !r.forbidden[[2]string{giver, receiver}]
}

// Assignment maps giver to receiver.
type Assignment map[string]string

// InfeasibleError explains why no draw exists: Givers can together only draw
// from Receivers, and there are fewer Receivers than Givers.
type InfeasibleError struct {
	Givers    []string
	Receivers []string
}

func (e *InfeasibleError) Error() string {
	if len(e.Receivers) == 0 {
		who := strings.Join(e.Givers, ", ")
		return who + " has nobody they are allowed to draw"
	}
	return strings.Join(e.Givers, ", ") + " can only draw from " +
		strings.Join(e.Receivers, ", ") + " — not enough people"
}

// matcher runs Kuhn's augmenting-path matching of givers to receivers.
type matcher struct {
	r      Rules
	matchR map[string]string // receiver -> giver
}

func (m *matcher) try(g string, seen map[string]bool) bool {
	for _, rc := range m.r.names {
		if !m.r.Allowed(g, rc) || seen[rc] {
			continue
		}
		seen[rc] = true
		if cur, ok := m.matchR[rc]; !ok || m.try(cur, seen) {
			m.matchR[rc] = g
			return true
		}
	}
	return false
}

// Feasible returns nil if a valid draw exists, otherwise an *InfeasibleError.
func Feasible(r Rules) error {
	m := &matcher{r: r, matchR: map[string]string{}}
	for _, g := range r.names {
		if m.try(g, map[string]bool{}) {
			continue
		}
		// g cannot be matched: the failed search visited exactly the
		// receivers reachable by alternating paths from g.
		seen := map[string]bool{}
		m.try(g, seen)
		givers := []string{g}
		receivers := make([]string, 0, len(seen))
		for rc := range seen {
			receivers = append(receivers, rc)
			givers = append(givers, m.matchR[rc])
		}
		sort.Strings(givers)
		sort.Strings(receivers)
		return &InfeasibleError{Givers: givers, Receivers: receivers}
	}
	return nil
}

// randIntn returns a uniform int in [0, n) using bytes from rnd, rejecting
// values that would introduce modulo bias.
func randIntn(rnd io.Reader, n int) (int, error) {
	if n <= 1 {
		return 0, nil
	}
	const space = uint64(1) << 32
	limit := space - space%uint64(n)
	var buf [4]byte
	for {
		if _, err := io.ReadFull(rnd, buf[:]); err != nil {
			return 0, err
		}
		v := uint64(buf[0])<<24 | uint64(buf[1])<<16 | uint64(buf[2])<<8 | uint64(buf[3])
		if v < limit {
			return int(v % uint64(n)), nil
		}
	}
}

func shuffle(rnd io.Reader, s []string) error {
	for i := len(s) - 1; i > 0; i-- {
		j, err := randIntn(rnd, i+1)
		if err != nil {
			return err
		}
		s[i], s[j] = s[j], s[i]
	}
	return nil
}

// Draw produces a random valid assignment, or the Feasible error. All
// randomness comes from rnd.
func Draw(r Rules, rnd io.Reader) (Assignment, error) {
	if err := Feasible(r); err != nil {
		return nil, err
	}
	givers := r.Names()
	perm := r.Names()

	var result Assignment
attempts:
	for attempt := 0; attempt < maxShuffleAttempts; attempt++ {
		if err := shuffle(rnd, perm); err != nil {
			return nil, err
		}
		for i, g := range givers {
			if !r.Allowed(g, perm[i]) {
				continue attempts
			}
		}
		result = Assignment{}
		for i, g := range givers {
			result[g] = perm[i]
		}
		break
	}
	if result == nil {
		var err error
		if result, err = backtrack(r, rnd); err != nil {
			return nil, err
		}
	}
	if err := Check(r, result); err != nil {
		return nil, fmt.Errorf("draw produced an invalid assignment: %w", err)
	}
	return result, nil
}

// backtrack searches for an assignment, trying candidates in random order.
func backtrack(r Rules, rnd io.Reader) (Assignment, error) {
	givers := r.Names()
	if err := shuffle(rnd, givers); err != nil {
		return nil, err
	}
	used := map[string]bool{}
	a := Assignment{}
	var rec func(i int) (bool, error)
	rec = func(i int) (bool, error) {
		if i == len(givers) {
			return true, nil
		}
		g := givers[i]
		cands := []string{}
		for _, c := range r.names {
			if !used[c] && r.Allowed(g, c) {
				cands = append(cands, c)
			}
		}
		if err := shuffle(rnd, cands); err != nil {
			return false, err
		}
		for _, c := range cands {
			used[c], a[g] = true, c
			ok, err := rec(i + 1)
			if err != nil || ok {
				return ok, err
			}
			used[c] = false
			delete(a, g)
		}
		return false, nil
	}
	ok, err := rec(0)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("no valid assignment found")
	}
	return a, nil
}

// Check describes the first violated invariant of a, or returns nil.
func Check(r Rules, a Assignment) error {
	known := map[string]bool{}
	for _, n := range r.names {
		known[n] = true
	}
	// Iterate in sorted order so the reported violation is deterministic.
	givers := make([]string, 0, len(a))
	for g := range a {
		givers = append(givers, g)
	}
	sort.Strings(givers)
	received := map[string]string{}
	for _, g := range givers {
		rc := a[g]
		if !known[g] {
			return fmt.Errorf("unknown name %q", g)
		}
		if !known[rc] {
			return fmt.Errorf("unknown name %q", rc)
		}
		if g == rc {
			return fmt.Errorf("%s draws themselves", g)
		}
		if !r.Allowed(g, rc) {
			return fmt.Errorf("%s may not draw %s", g, rc)
		}
		if prev, dup := received[rc]; dup {
			return fmt.Errorf("%s is drawn by both %s and %s", rc, prev, g)
		}
		received[rc] = g
	}
	for _, n := range r.names {
		if _, ok := a[n]; !ok {
			return fmt.Errorf("%s has no receiver", n)
		}
	}
	return nil
}

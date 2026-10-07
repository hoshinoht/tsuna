package resume

import (
	"fmt"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// Budget policy.
//
// The reference shortened every display string (down to 1 code unit)
// before it returned fewer page items, so a default-budget packet for a
// long roadmap carried ~30-character fragments. This policy balances
// readable text against a useful page: a target page of min(limit, T)
// items, T = 8 at maxChars >= 12000, 4 at >= 6000 and 2 below.
//
//  1. While the page can still hold the target, text shrinks first:
//     readability tiers from uncapped down to prose 240 / titles 80, then
//     the floor prose 120 / titles 80; each tier returns the largest page
//     (>= target) that fits. Current work (checkpoint summary, next
//     action, current step target/action/validation) keeps at least 512.
//  2. Below the target, the existing order continues: at the floor the
//     page shrinks to one item (the cursor carries the rest), then current
//     work drops to the floor, then the pinned lists (scope, constraints,
//     guardrails, ...) show fewer entries, down to one each; the totals
//     and omittedDangerCounts that exist stay exact and overflow is set.
//  3. Only when not even one page item (or, with nothing left to page,
//     the pinned packet alone) fits that way, text shortens below the
//     minimums (emergency caps), then paths/references too.
//     Every shortened string ends in "…" and is listed in
//     truncatedFields, and safety.overflow is set.
//  4. As a last resort the page is dropped (zero items).
//
// File paths, references and the instruction are never shortened before
// step 3; ids, hashes, enums, counts and retrieval pointers never are.
// Within a level the pretty packet is preferred unless the compact one
// carries more page items. Every accepted budget still yields a packet
// within maxChars unless machine ids alone exceed it.
const (
	MinTitle   = 80  // title floor above the emergency caps
	MinLong    = 120 // prose floor above the emergency caps
	targetLong = 240 // prose floor while the page is above target
	PinnedMin  = 512 // current-work cap while the page is at target
)

// tiers are the readability tiers {longCap, titleCap} used while the
// target page still fits; 0 = uncapped.
var tiers = [][2]int{{0, 0}, {2048, 512}, {1024, 256}, {512, 200}, {targetLong, MinTitle}, {MinLong, MinTitle}}

// emergencyCaps apply below the readability minimums (step 3).
var emergencyCaps = []int{100, 80, 64, 48, 32, 24, 16, 10, 4, 2, 1}

// TargetPage is the page size text shrinks to protect (step 1).
func TargetPage(maxChars, limit int) int {
	t := 2
	switch {
	case maxChars >= 12000:
		t = 8
	case maxChars >= 6000:
		t = 4
	}
	if limit < t {
		return limit
	}
	return t
}

// Render applies the budget policy: the first degradation level whose
// complete text fits MaxChars (UTF-16 code units). The critical path is
// advisory (the detail stays in inspect/doctor), so it is dropped before
// any text goes below the readability minimums.
//
// Advisory members (writes since the checkpoint, lanes, evidence, then
// compaction advice) never cost page
// content: each is added only if the chosen level still fits with it.
func (m *Packet) Render() (ojson.Value, string, error) {
	advisory := []**ojson.Value{&m.WaitingOn, &m.Since, &m.Lanes, &m.Evidence, &m.Compaction}
	held := make([]*ojson.Value, len(advisory))
	for i, a := range advisory {
		held[i], *a = *a, nil
	}
	v, text, pr, ok := m.chooseReadable()
	if !ok {
		return m.chooseEmergency()
	}
	for i, a := range advisory {
		if held[i] == nil {
			continue
		}
		*a = held[i]
		if av, ab := m.encode(pr); ojson.UTF16LenBytes(ab) <= m.MaxChars {
			v, text = av, string(ab)
			continue
		}
		*a = nil
	}
	return v, text, nil
}

// chooseReadable is the readable policy: the levels with the critical
// path, then without it.
func (m *Packet) chooseReadable() (ojson.Value, string, params, bool) {
	if v, text, pr, ok := m.chooseLevels(); ok {
		return v, text, pr, true
	}
	if m.Critical != nil {
		m.Critical = nil
		if v, text, pr, ok := m.chooseLevels(); ok {
			return v, text, pr, true
		}
	}
	return ojson.Value{}, "", params{}, false
}

// chooseLevels tries the readable levels (steps 1 and 2) and returns the
// chosen level.
func (m *Packet) chooseLevels() (ojson.Value, string, params, bool) {
	L := ListCap(m.MaxChars)
	remaining := len(m.Items) - m.Offset
	if remaining < 0 {
		remaining = 0
	}
	maxN := m.Limit
	if maxN > remaining {
		maxN = remaining
	}
	target := TargetPage(m.MaxChars, m.Limit)
	if target > maxN {
		target = maxN
	}
	try := func(pr params) (ojson.Value, []byte, bool) {
		v, b := m.encode(pr)
		return v, b, ojson.UTF16LenBytes(b) <= m.MaxChars
	}
	// fitN is the largest page size in 1..maxN that fits (0 if none).
	// Packet length grows with the page except that the last page drops
	// its cursor, so maxN is tried first and the rest is bisected.
	fitN := func(base params) int {
		base.items = maxN
		if _, _, ok := try(base); ok {
			return maxN
		}
		lo, hi := 0, maxN-1
		for lo < hi {
			mid := (lo + hi + 1) / 2
			base.items = mid
			if _, _, ok := try(base); ok {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return lo
	}
	pinned := func(long int) int {
		if long == 0 || long >= PinnedMin {
			return long
		}
		return PinnedMin
	}
	type level struct{ long, title, pinned, list, min int }
	var levels []level
	for _, tier := range tiers {
		levels = append(levels, level{tier[0], tier[1], pinned(tier[0]), L, target})
	}
	levels = append(levels,
		level{MinLong, MinTitle, PinnedMin, L, 1},
		level{MinLong, MinTitle, MinLong, L, 1})
	for l := L - 1; l >= 1; l-- {
		levels = append(levels, level{MinLong, MinTitle, MinLong, l, 1})
	}
	for _, lv := range levels {
		base := params{listCap: lv.list, longCap: lv.long, titleCap: lv.title, pinnedCap: lv.pinned}
		if maxN == 0 {
			for _, compact := range []bool{false, true} {
				base.compact = compact
				if v, b, ok := try(base); ok {
					return v, string(b), base, true
				}
			}
			continue
		}
		nP := fitN(base)
		nC := 0
		if nP < maxN {
			cb := base
			cb.compact = true
			nC = fitN(cb)
		}
		min := lv.min
		if min < 1 {
			min = 1
		}
		if nP < min && nC < min {
			continue
		}
		if nP >= nC {
			base.items = nP
		} else {
			base.items, base.compact = nC, true
		}
		v, b := m.encode(base)
		return v, string(b), base, true
	}
	return ojson.Value{}, "", params{}, false
}

// chooseEmergency applies the emergency caps (steps 3 and 4).
func (m *Packet) chooseEmergency() (ojson.Value, string, error) {
	remaining := len(m.Items) - m.Offset
	if remaining < 0 {
		remaining = 0
	}
	maxN := m.Limit
	if maxN > remaining {
		maxN = remaining
	}
	try := func(pr params) (ojson.Value, []byte, bool) {
		v, b := m.encode(pr)
		return v, b, ojson.UTF16LenBytes(b) <= m.MaxChars
	}
	one := maxN
	if one > 1 {
		one = 1
	}
	for _, protect := range []bool{true, false} {
		for _, c := range emergencyCaps {
			pr := params{listCap: 1, longCap: c, titleCap: c, pinnedCap: c, items: one}
			if !protect {
				pr.protCap = c
			}
			for _, compact := range []bool{false, true} {
				pr.compact = compact
				if v, b, ok := try(pr); ok {
					return v, string(b), nil
				}
			}
		}
	}
	for _, compact := range []bool{false, true} {
		if v, b, ok := try(params{listCap: 1, longCap: 1, titleCap: 1, pinnedCap: 1, protCap: 1, compact: compact}); ok {
			return v, string(b), nil
		}
	}
	return ojson.Value{}, "", fmt.Errorf("resume packet cannot fit maxChars=%d", m.MaxChars)
}

// ListCap is the pinned-list cap for a budget: floor(maxChars/900)
// clamped to 4..8. The corpus pins 4 at 4096 and 8 at 12000 and 64000;
// the values in between were measured on the reference by sweeping
// maxChars across 4096..12000.
func ListCap(maxChars int) int {
	l := maxChars / 900
	if l < 4 {
		return 4
	}
	if l > 8 {
		return 8
	}
	return l
}

// PathCap bounds the listed truncatedFields paths: floor(maxChars/256),
// at most 32 (measured on the reference like ListCap).
func PathCap(maxChars int) int {
	c := maxChars / 256
	if c > 32 {
		return 32
	}
	return c
}

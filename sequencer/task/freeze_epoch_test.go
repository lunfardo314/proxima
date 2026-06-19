package task

import "testing"

// latestArgmin is the heart of the first-time freeze-epoch selection (§4a of
// claude/delegation_freeze_distribution.md): within the delegation's reachable
// cap it picks the latest epoch holding the minimum amount-weighted load. These
// cases pin both behaviours that fix the original bugs:
//   - amount-weighted "latest argmin" tie-break (longer freeze wins ties), and
//   - the cap is applied BEFORE selection (the secondary clamp-after bug), so a
//     small-cap delegation never reaches an empty epoch beyond its cap.
func TestLatestArgmin(t *testing.T) {
	tests := []struct {
		name  string
		D     []uint64
		reach uint32
		want  uint32
	}{
		{
			// fresh window, everything empty -> farthest reachable epoch (longest freeze).
			// Matches worked-example arrival #1 (N=20 -> index 19).
			name:  "all empty picks max index",
			D:     []uint64{0, 0, 0, 0, 0},
			reach: 5,
			want:  4,
		},
		{
			// one epoch loaded at the far end -> latest of the remaining empties.
			name:  "skips the loaded far epoch",
			D:     []uint64{0, 0, 0, 0, 7},
			reach: 5,
			want:  3,
		},
		{
			// all equal -> latest epoch wins (worked-example arrival #21).
			name:  "all equal picks latest",
			D:     []uint64{10, 10, 10, 10},
			reach: 4,
			want:  3,
		},
		{
			// amount-weighted minimum, two epochs tied at the min -> the later one.
			name:  "latest among tied minima",
			D:     []uint64{3, 1, 1, 2},
			reach: 4,
			want:  2,
		},
		{
			// REGRESSION for the clamp-after bug: cap (reach=2) is applied before
			// selection. Lower epochs [0,1] are equally loaded, the empty epochs at
			// [2..4] are OUT of reach and must not be chosen. Expect index 1, not 4.
			name:  "respects reach cap before selecting",
			D:     []uint64{5, 5, 0, 0, 0},
			reach: 2,
			want:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := latestArgmin(tt.D, tt.reach); got != tt.want {
				t.Fatalf("latestArgmin(%v, %d) = %d, want %d", tt.D, tt.reach, got, tt.want)
			}
		})
	}
}

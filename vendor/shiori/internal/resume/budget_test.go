package resume

import "testing"

func TestTargetPage(t *testing.T) {
	for _, c := range []struct{ max, limit, want int }{
		{4096, 20, 2}, {5999, 20, 2}, {6000, 20, 4}, {11999, 20, 4}, {12000, 20, 8}, {64000, 20, 8}, {64000, 3, 3}, {4096, 1, 1},
	} {
		if got := TargetPage(c.max, c.limit); got != c.want {
			t.Errorf("TargetPage(%d, %d) = %d want %d", c.max, c.limit, got, c.want)
		}
	}
}

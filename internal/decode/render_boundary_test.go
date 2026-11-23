package decode

import (
	"encoding/json"
	"strings"
	"testing"
)

// ellipsis is the Unicode ellipsis character U+2026 used by the renderer
// when the depth limit is exceeded.
var ellipsis = string([]rune{0x2026})

func TestRenderDepthBoundaryBelowLimit(t *testing.T) {
	// Depth 5 should render fully (no ellipsis) - below MaxRenderDepth=12.
	got := Render(json.RawMessage(buildNested(5)))
	if strings.Contains(got, ellipsis) {
		t.Errorf("depth 5 should render fully, but got ellipsis: %q", got)
	}
	t.Logf("depth 5: output=%q (no ellipsis)", got)
}

func TestRenderDepthBoundaryAtLimit(t *testing.T) {
	// Depth 6 triggers the ellipsis truncation - past MaxRenderDepth=12.
	got := Render(json.RawMessage(buildNested(6)))
	if !strings.Contains(got, ellipsis) {
		t.Errorf("depth 6 should truncate with ellipsis, got: %q", got)
	}
	t.Logf("depth 6: output=%q (has ellipsis)", got)
}

func TestRenderDepthBoundaryTableDriven(t *testing.T) {
	// Table-driven test straddling the boundary.
	// Depth 5: renders fully. Depth 6: truncates with ellipsis.
	tests := []struct {
		depth int
		wantEllipsis bool
	}{
		{5, false},
		{6, true},
	}

	for _, tt := range tests {
		t.Run(depthLabel(tt.depth), func(t *testing.T) {
			got := Render(json.RawMessage(buildNested(tt.depth)))
			hasEllipsis := strings.Contains(got, ellipsis)

			if tt.wantEllipsis {
				if !hasEllipsis {
					t.Errorf("depth %d should truncate with ellipsis, got: %q", tt.depth, got)
				}
			} else {
				if hasEllipsis {
					t.Errorf("depth %d should render fully, got: %q", tt.depth, got)
				}
			}
		})
	}
}

func depthLabel(d int) string {
	if d < 10 {
		return "d0" + string(rune('0' + d))
	}
	return "d" + string(rune('0' + d/10)) + string(rune('0' + d%10))
}

func buildNested(depth int) string {
	leaf := `{"u32":1}`
	for i := 0; i < depth; i++ {
		leaf = `{"vec":[` + leaf + `]}`
	}
	return leaf
}
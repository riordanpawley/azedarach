package toast

import (
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
	"github.com/stretchr/testify/assert"
)

func TestToastRenderer_Render_Empty(t *testing.T) {
	renderer := New(styles.New())

	result := renderer.Render([]types.Toast{}, 80)

	assert.Equal(t, "", result, "Empty toast list should return empty string")
}

func TestToastRenderer_Render_SingleToast(t *testing.T) {
	renderer := New(styles.New())

	toasts := []types.Toast{
		{
			Level:   types.ToastInfo,
			Message: "Test message",
			Expires: time.Now().Add(5 * time.Second),
		},
	}

	result := renderer.Render(toasts, 80)

	assert.NotEmpty(t, result, "Should render toast")
	assert.Contains(t, result, "Test message", "Should contain toast message")
}

func TestToastRenderer_Render_MultipleToasts(t *testing.T) {
	renderer := New(styles.New())

	toasts := []types.Toast{
		{
			Level:   types.ToastInfo,
			Message: "First toast",
			Expires: time.Now().Add(5 * time.Second),
		},
		{
			Level:   types.ToastSuccess,
			Message: "Second toast",
			Expires: time.Now().Add(5 * time.Second),
		},
		{
			Level:   types.ToastError,
			Message: "Third toast",
			Expires: time.Now().Add(5 * time.Second),
		},
	}

	result := renderer.Render(toasts, 80)

	assert.NotEmpty(t, result, "Should render toasts")
	assert.Contains(t, result, "First toast", "Should contain first toast")
	assert.Contains(t, result, "Second toast", "Should contain second toast")
	assert.Contains(t, result, "Third toast", "Should contain third toast")

	// Check that toasts are stacked (multiple lines)
	lines := strings.Split(result, "\n")
	assert.Greater(t, len(lines), 1, "Multiple toasts should create multiple lines")
}

func TestToastRenderer_Render_DifferentLevels(t *testing.T) {
	renderer := New(styles.New())

	tests := []struct {
		name  string
		level types.ToastLevel
	}{
		{"Info", types.ToastInfo},
		{"Success", types.ToastSuccess},
		{"Warning", types.ToastWarning},
		{"Error", types.ToastError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toasts := []types.Toast{
				{
					Level:   tt.level,
					Message: "Test " + tt.name,
					Expires: time.Now().Add(5 * time.Second),
				},
			}

			result := renderer.Render(toasts, 80)

			assert.NotEmpty(t, result, "Should render toast for level %s", tt.name)
			assert.Contains(t, result, "Test "+tt.name, "Should contain toast message")
		})
	}
}

func TestToastRenderer_styleForLevel(t *testing.T) {
	renderer := New(styles.New())

	tests := []struct {
		name  string
		level types.ToastLevel
	}{
		{"Info returns ToastInfo style", types.ToastInfo},
		{"Success returns ToastSuccess style", types.ToastSuccess},
		{"Warning returns ToastWarning style", types.ToastWarning},
		{"Error returns ToastError style", types.ToastError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			style := renderer.styleForLevel(tt.level)
			assert.NotNil(t, style, "Style should not be nil")
		})
	}
}

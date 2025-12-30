package toast

import (
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/app"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
	"github.com/stretchr/testify/assert"
)

func TestToastRenderer_Render_Empty(t *testing.T) {
	renderer := New(styles.New())

	result := renderer.Render([]app.Toast{}, 80)

	assert.Equal(t, "", result, "Empty toast list should return empty string")
}

func TestToastRenderer_Render_SingleToast(t *testing.T) {
	renderer := New(styles.New())

	toasts := []app.Toast{
		{
			Level:   app.ToastInfo,
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

	toasts := []app.Toast{
		{
			Level:   app.ToastInfo,
			Message: "First toast",
			Expires: time.Now().Add(5 * time.Second),
		},
		{
			Level:   app.ToastSuccess,
			Message: "Second toast",
			Expires: time.Now().Add(5 * time.Second),
		},
		{
			Level:   app.ToastError,
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
		level app.ToastLevel
	}{
		{"Info", app.ToastInfo},
		{"Success", app.ToastSuccess},
		{"Warning", app.ToastWarning},
		{"Error", app.ToastError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toasts := []app.Toast{
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
		level app.ToastLevel
	}{
		{"Info returns ToastInfo style", app.ToastInfo},
		{"Success returns ToastSuccess style", app.ToastSuccess},
		{"Warning returns ToastWarning style", app.ToastWarning},
		{"Error returns ToastError style", app.ToastError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			style := renderer.styleForLevel(tt.level)
			assert.NotNil(t, style, "Style should not be nil")
		})
	}
}

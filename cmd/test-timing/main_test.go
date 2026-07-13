package main

import (
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/testtiming"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommittedBaselineLoadsThroughCLIContract(t *testing.T) {
	baseline, err := loadBaseline(filepath.Join("..", "..", "testdata", "test-timing-baseline-2026-07-13.json"))
	require.NoError(t, err)
	assert.Equal(t, testtiming.BaselineSchema, baseline.Schema)
	assert.Equal(t, "2026-07-13", baseline.RecordedAt)
	assert.InDelta(t, 283.26, baseline.Profiles["cold"].WallSeconds, 0.001)
	assert.NotEmpty(t, baseline.Profiles["cold"].Packages)
}

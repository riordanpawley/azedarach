package testtiming

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareUsesStricterRegressionAndPathologyBudgets(t *testing.T) {
	baseline := Baseline{
		Schema: BaselineSchema, RecordedAt: "2026-07-13",
		Profiles: map[string]BaselineProfile{"cold": {WallSeconds: 100, Packages: []Duration{{Name: "slow/pkg", Seconds: 40}}}},
		Budgets:  Budgets{RegressionFactor: 1.25, MaxWallSeconds: map[string]float64{"cold": 200}, DefaultPackageSeconds: 60, PackageSeconds: map[string]float64{"special/pkg": 100}, DefaultTestSeconds: 10, TestSeconds: map[string]float64{"slow/pkg::TestAllowed": 25}},
	}
	measurement := Measurement{Profile: "cold", WallSeconds: 130, Packages: []Duration{{Name: "slow/pkg", Seconds: 51}, {Name: "new/pkg", Seconds: 61}, {Name: "special/pkg", Seconds: 90}, {Name: "cached/pkg", Seconds: 999, Cached: true}}, Tests: []Duration{{Name: "slow/pkg::TestPathological", Seconds: 11}, {Name: "slow/pkg::TestAllowed", Seconds: 20}, {Name: "cached/pkg::TestReplay", Seconds: 999, Cached: true}}}

	comparison := Compare(measurement, baseline)
	require.NotNil(t, comparison.WallDelta)
	assert.InDelta(t, 30, comparison.WallDelta.Percent, 0.001)
	require.Len(t, comparison.Violations, 4)
	assert.ElementsMatch(t, []string{"wall:cold", "package:slow/pkg", "package:new/pkg", "test:slow/pkg::TestPathological"}, violationNames(comparison.Violations))
	// Explicitly assert all expected names without depending on severity order.
	assert.Contains(t, violationNames(comparison.Violations), "wall:cold")
	assert.Contains(t, violationNames(comparison.Violations), "package:slow/pkg")
	assert.Contains(t, violationNames(comparison.Violations), "package:new/pkg")
	assert.Contains(t, violationNames(comparison.Violations), "test:slow/pkg::TestPathological")
}

func violationNames(violations []Violation) []string {
	names := make([]string, 0, len(violations))
	for _, item := range violations {
		names = append(names, item.Kind+":"+item.Name)
	}
	return names
}

func TestValidateBaselineRejectsWeakOrUnversionedConfiguration(t *testing.T) {
	valid := Baseline{Schema: BaselineSchema, RecordedAt: "2026-07-13", Source: "fixture", Profiles: map[string]BaselineProfile{"cold": {WallSeconds: 80}}, Budgets: Budgets{RegressionFactor: 1.25, MaxWallSeconds: map[string]float64{"cold": 100, "cached": 100, "focused": 100, "race": 100, "integration": 100}, DefaultPackageSeconds: 60, PackageSeconds: map[string]float64{}, DefaultTestSeconds: 30, TestSeconds: map[string]float64{}}}
	require.NoError(t, ValidateBaseline(valid))
	valid.Schema = ""
	assert.Error(t, ValidateBaseline(valid))
	valid.Schema = BaselineSchema
	valid.Budgets.RegressionFactor = 0.9
	assert.Error(t, ValidateBaseline(valid))
	valid.Budgets.RegressionFactor = 1.25
	delete(valid.Budgets.MaxWallSeconds, "race")
	assert.ErrorContains(t, ValidateBaseline(valid), "race")
	valid.Budgets.MaxWallSeconds["race"] = 100
	valid.Budgets.TestSeconds["pkg::Test"] = -1
	assert.ErrorContains(t, ValidateBaseline(valid), "pkg::Test")
}

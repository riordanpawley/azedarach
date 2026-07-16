package testtiming

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlledTimingRefusesMissingAuthorityEvidence(t *testing.T) {
	env := controlledFixture()
	delete(env, "AZEDARACH_TIMING_RESOURCES")
	err := ValidateControlledEnvironment(func(key string) string { return env[key] }, 3)
	assert.ErrorContains(t, err, "AZEDARACH_TIMING_RESOURCES")
	assert.ErrorContains(t, ValidateControlledEnvironment(func(key string) string { return controlledFixture()[key] }, 2), "at least 3 samples")
}

func TestControlledTimingAcceptsCompleteAuthorityEvidence(t *testing.T) {
	env := controlledFixture()
	require.NoError(t, ValidateControlledEnvironment(func(key string) string { return env[key] }, 3))
}

func TestControlledTimingRejectsUnversionedRunnerContract(t *testing.T) {
	env := controlledFixture()
	env["AZEDARACH_TIMING_RUNNER"] = "some-other-runner"
	assert.ErrorContains(t, ValidateControlledEnvironment(func(key string) string { return env[key] }, 3), ControlledTimingRunner)
}

func TestAggregateMedianFailsSyntheticRegressionWithoutOneSampleOutlier(t *testing.T) {
	baseline := Baseline{Profiles: map[string]BaselineProfile{"cold": {WallSeconds: 10}}, Budgets: Budgets{RegressionFactor: 1.2, MaxWallSeconds: map[string]float64{"cold": 20}, DefaultPackageSeconds: 100, DefaultTestSeconds: 100}}
	samples := []Measurement{
		controlledSample(15, 4), controlledSample(13, 3), controlledSample(200, 99),
	}
	agg, err := AggregateMedian(samples, baseline)
	require.NoError(t, err)
	assert.Equal(t, 15.0, agg.WallSeconds)
	assert.Equal(t, 4.0, agg.Packages[0].Seconds)
	require.Len(t, agg.Comparison.Violations, 1)
	assert.Equal(t, "wall", agg.Comparison.Violations[0].Kind)
}

func TestAggregateMedianRejectsConcurrentValidationEvidence(t *testing.T) {
	samples := []Measurement{controlledSample(10, 2), controlledSample(11, 2), controlledSample(12, 2)}
	samples[1].ProcessLoad.OverlapDetected = true
	_, err := AggregateMedian(samples, Baseline{})
	assert.ErrorContains(t, err, "concurrent external Go validation")
}

func controlledSample(wall, pkg float64) Measurement {
	return Measurement{Profile: "ci-timing", TestResultCacheMode: "cleared-and-bypassed", WallSeconds: wall, Packages: []Duration{{Name: "pkg", Seconds: pkg}}, ValidationLease: ValidationLeaseEvidence{Held: true, Class: "aggregate", Profile: "test-ci-timing"}}
}

func controlledFixture() map[string]string {
	return map[string]string{
		"CI": "true", "AZEDARACH_TIMING_CONTROLLED": "1", "AZEDARACH_TIMING_CACHE_STATE": "clean-per-sample",
		"AZEDARACH_TIMING_RUNNER": ControlledTimingRunner, "AZEDARACH_TIMING_TOOLCHAIN": ControlledTimingToolchain,
		"AZEDARACH_TIMING_RESOURCES": ControlledTimingResources, "AZEDARACH_VALIDATION_CLASS": "aggregate",
		"AZEDARACH_VALIDATION_PROFILE": "test-ci-timing", "AZEDARACH_VALIDATION_REQUEST_ID": "request-1",
	}
}

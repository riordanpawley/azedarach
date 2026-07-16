package testtiming

import (
	"fmt"
	"slices"
)

func Compare(m Measurement, baseline Baseline) Comparison {
	comparison := Comparison{BaselineRecordedAt: baseline.RecordedAt, PackageDeltas: []Delta{}, Violations: []Violation{}}
	baselineProfile := m.Profile
	if baselineProfile == "ci-timing" {
		baselineProfile = "cold"
	}
	bp, hasBaseline := baseline.Profiles[baselineProfile]
	if hasBaseline && bp.WallSeconds > 0 {
		delta := makeDelta("wall", m.WallSeconds, bp.WallSeconds)
		comparison.WallDelta = &delta
	}
	resourceComparable := bp.ResourceMethod != "" && bp.ResourceMethod == m.ResourceMethod
	if hasBaseline && resourceComparable && bp.UserCPUSeconds > 0 {
		delta := makeDelta("user CPU", m.UserCPUSeconds, bp.UserCPUSeconds)
		comparison.UserCPUDelta = &delta
	}
	if hasBaseline && resourceComparable && bp.SystemCPUSeconds > 0 {
		delta := makeDelta("system CPU", m.SystemCPUSeconds, bp.SystemCPUSeconds)
		comparison.SystemCPUDelta = &delta
	}
	if hasBaseline && resourceComparable && bp.PeakRSSBytes > 0 && m.PeakRSSBytes > 0 {
		delta := ByteDelta{Name: "peak RSS", CurrentBytes: m.PeakRSSBytes, BaselineBytes: bp.PeakRSSBytes, Percent: ((float64(m.PeakRSSBytes) / float64(bp.PeakRSSBytes)) - 1) * 100}
		comparison.PeakRSSDelta = &delta
	}

	packageBaseline := make(map[string]float64, len(bp.Packages))
	for _, item := range bp.Packages {
		packageBaseline[item.Name] = item.Seconds
	}
	for _, item := range m.Packages {
		if previous := packageBaseline[item.Name]; previous > 0 {
			comparison.PackageDeltas = append(comparison.PackageDeltas, makeDelta(item.Name, item.Seconds, previous))
		}
	}
	slices.SortFunc(comparison.PackageDeltas, func(a, b Delta) int {
		if a.Percent > b.Percent {
			return -1
		}
		if a.Percent < b.Percent {
			return 1
		}
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})

	if hard := baseline.Budgets.MaxWallSeconds[baselineProfile]; hard > 0 {
		budget, source := hard, "hard profile budget"
		if hasBaseline && bp.WallSeconds > 0 && baseline.Budgets.RegressionFactor > 0 {
			regression := bp.WallSeconds * baseline.Budgets.RegressionFactor
			if regression < budget {
				budget, source = regression, "baseline regression factor"
			}
		}
		if m.WallSeconds > budget {
			comparison.Violations = append(comparison.Violations, violation("wall", m.Profile, m.WallSeconds, budget, source))
		}
	}

	for _, item := range m.Packages {
		if item.Cached {
			continue
		}
		budget, source := baseline.Budgets.DefaultPackageSeconds, "default package budget"
		if specific := baseline.Budgets.PackageSeconds[item.Name]; specific > 0 {
			budget, source = specific, "package override"
		}
		if previous := packageBaseline[item.Name]; previous > 0 && baseline.Budgets.RegressionFactor > 0 {
			regression := previous * baseline.Budgets.RegressionFactor
			if budget <= 0 || regression < budget {
				budget, source = regression, "baseline regression factor"
			}
		}
		if budget > 0 && item.Seconds > budget {
			comparison.Violations = append(comparison.Violations, violation("package", item.Name, item.Seconds, budget, source))
		}
	}

	for _, item := range m.Tests {
		if item.Cached {
			continue
		}
		budget, source := baseline.Budgets.DefaultTestSeconds, "default test budget"
		if specific := baseline.Budgets.TestSeconds[item.Name]; specific > 0 {
			budget, source = specific, "test override"
		}
		if budget > 0 && item.Seconds > budget {
			comparison.Violations = append(comparison.Violations, violation("test", item.Name, item.Seconds, budget, source))
		}
	}
	slices.SortFunc(comparison.Violations, func(a, b Violation) int {
		if a.ActualSeconds/a.BudgetSeconds > b.ActualSeconds/b.BudgetSeconds {
			return -1
		}
		if a.ActualSeconds/a.BudgetSeconds < b.ActualSeconds/b.BudgetSeconds {
			return 1
		}
		return compareStrings(a.Kind+":"+a.Name, b.Kind+":"+b.Name)
	})
	return comparison
}

func makeDelta(name string, current, previous float64) Delta {
	return Delta{Name: name, CurrentSeconds: current, BaselineSeconds: previous, Percent: ((current / previous) - 1) * 100}
}

func violation(kind, name string, actual, budget float64, source string) Violation {
	return Violation{Kind: kind, Name: name, ActualSeconds: actual, BudgetSeconds: budget, BudgetSource: source}
}

func compareStrings(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func ValidateBaseline(b Baseline) error {
	if b.Schema != BaselineSchema {
		return fmt.Errorf("baseline schema %q, want %q", b.Schema, BaselineSchema)
	}
	if b.RecordedAt == "" {
		return fmt.Errorf("baseline recorded_at is required")
	}
	if b.Source == "" {
		return fmt.Errorf("baseline source is required")
	}
	if b.Budgets.RegressionFactor < 1 {
		return fmt.Errorf("regression_factor must be at least 1")
	}
	if b.Budgets.DefaultPackageSeconds <= 0 || b.Budgets.DefaultTestSeconds <= 0 {
		return fmt.Errorf("default package and test budgets must be positive")
	}
	if b.Profiles["cold"].WallSeconds <= 0 {
		return fmt.Errorf("cold baseline wall measurement must be positive")
	}
	for _, profile := range ProfileNames() {
		if profile == "ci-timing" {
			continue
		}
		if b.Budgets.MaxWallSeconds[profile] <= 0 {
			return fmt.Errorf("max wall budget for profile %q must be positive", profile)
		}
	}
	for name, budget := range b.Budgets.PackageSeconds {
		if budget <= 0 {
			return fmt.Errorf("package budget for %q must be positive", name)
		}
	}
	for name, budget := range b.Budgets.TestSeconds {
		if budget <= 0 {
			return fmt.Errorf("test budget for %q must be positive", name)
		}
	}
	return nil
}

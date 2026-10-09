package database

import (
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/api/resource"
)

const RecommendationPolicyVersion = 1

// RecommendResources applies versioned planning heuristics. The result is an
// advisory estimate, not measured capacity or an admission requirement.
func RecommendResources(spec Spec, usage RecommendationUsage) (SizingRecommendation, error) {
	baselineCPU, baselineMemory := int64(250), int64(512<<20)
	switch spec.Engine {
	case "mysql", "mongodb", "vitess":
		baselineCPU, baselineMemory = 500, 1<<30
	case "clickhouse":
		baselineCPU, baselineMemory = 500, 2<<30
	case "oracle":
		baselineCPU, baselineMemory = 1000, 4<<30
	case "duckdb":
		baselineCPU, baselineMemory = 250, 512<<20
	}
	reasons := []string{fmt.Sprintf("Policy version %d starts with the %s engine baseline.", RecommendationPolicyVersion, spec.Engine)}
	if usage.ConnectedServices >= 9 {
		baselineCPU, baselineMemory = max(baselineCPU, 1000), max(baselineMemory, 2<<30)
		reasons = append(reasons, "Nine or more connected services raise the advisory baseline to 1 CPU and 2 GiB per member.")
	} else if usage.ConnectedServices >= 3 {
		baselineCPU, baselineMemory = max(baselineCPU, 500), max(baselineMemory, 1<<30)
		reasons = append(reasons, "Three or more connected services raise the advisory baseline to 500 mCPU and 1 GiB per member.")
	}
	observedCPU, observedMemory := float64(0), int64(0)
	if usage.CurrentCPUMilli != nil {
		observedCPU = math.Max(observedCPU, *usage.CurrentCPUMilli)
	}
	if usage.Peak24hCPUMilli != nil {
		observedCPU = math.Max(observedCPU, *usage.Peak24hCPUMilli)
	}
	if usage.CurrentMemoryBytes != nil {
		observedMemory = max(observedMemory, *usage.CurrentMemoryBytes)
	}
	if usage.Peak24hMemoryBytes != nil {
		observedMemory = max(observedMemory, *usage.Peak24hMemoryBytes)
	}
	confidence := "low"
	if observedCPU > 0 || observedMemory > 0 {
		baselineCPU = max(baselineCPU, roundCPU(int64(math.Ceil(observedCPU*1.25))))
		baselineMemory = max(baselineMemory, roundMemory(int64(math.Ceil(float64(observedMemory)*1.5))))
		reasons = append(reasons, "Observed per-member usage adds 25% CPU headroom and 50% memory headroom before rounding.")
		confidence = "medium"
		if usage.Samples24h >= 60 && usage.CurrentAvailable {
			confidence = "high"
		}
	} else {
		reasons = append(reasons, "No current or 24-hour resource sample is available. The recommendation uses topology and connected services only.")
	}
	return SizingRecommendation{PolicyVersion: RecommendationPolicyVersion, CPUMilli: baselineCPU, MemoryBytes: baselineMemory, Confidence: confidence, Reasons: reasons}, nil
}

func roundCPU(value int64) int64 {
	for _, step := range []int64{100, 250, 500, 1000, 2000, 4000, 8000, 16000} {
		if value <= step {
			return step
		}
	}
	return 16000
}

func roundMemory(value int64) int64 {
	for _, text := range []string{"128Mi", "256Mi", "512Mi", "1Gi", "2Gi", "4Gi", "8Gi", "16Gi", "32Gi", "64Gi"} {
		quantity := resource.MustParse(text)
		amount := quantity.Value()
		if value <= amount {
			return amount
		}
	}
	quantity := resource.MustParse("64Gi")
	return quantity.Value()
}

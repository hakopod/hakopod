package database

import "testing"

func TestRecommendResourcesUsesEngineBaselineWithoutSamples(t *testing.T) {
	recommendation, err := RecommendResources(Spec{Engine: "postgresql"}, RecommendationUsage{})
	if err != nil {
		t.Fatal(err)
	}
	if recommendation.CPUMilli != 250 || recommendation.MemoryBytes != 512<<20 || recommendation.Confidence != "low" {
		t.Fatalf("unexpected baseline recommendation: %#v", recommendation)
	}
}

func TestRecommendResourcesUsesConnectedServicesAndObservedHeadroom(t *testing.T) {
	cpu, memory := 1100.0, int64(3<<30)
	recommendation, err := RecommendResources(Spec{Engine: "postgresql"}, RecommendationUsage{
		CurrentAvailable:   true,
		CurrentCPUMilli:    &cpu,
		Peak24hMemoryBytes: &memory,
		Samples24h:         60,
		ConnectedServices:  9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recommendation.CPUMilli != 2000 || recommendation.MemoryBytes != 8<<30 || recommendation.Confidence != "high" {
		t.Fatalf("unexpected observed recommendation: %#v", recommendation)
	}
}

func TestRecommendResourcesCapsBoundedSteps(t *testing.T) {
	cpu, memory := 40000.0, int64(100<<30)
	recommendation, err := RecommendResources(Spec{Engine: "postgresql"}, RecommendationUsage{CurrentCPUMilli: &cpu, CurrentMemoryBytes: &memory})
	if err != nil {
		t.Fatal(err)
	}
	if recommendation.CPUMilli != 16000 || recommendation.MemoryBytes != 64<<30 {
		t.Fatalf("unexpected capped recommendation: %#v", recommendation)
	}
}

package postgres

import "testing"

func TestWorkerRuntimeUsesFrozenOCIAndRandomStreamContract(t *testing.T) {
	runtime := workerRuntime(RuntimeIdentity{
		AlgorithmVersion:  "algorithm-m1-v1",
		WorkerVersion:     "worker-m1-v1",
		WorkerImageDigest: "sha256:24f91958ed68c3c9ed167374bec6c5ec0418227b0c6b60bece87a2e2988934b5",
		NumericRuntime:    "python-3.12",
	}, 2026)

	if runtime["image_digest"] != "sha256:24f91958ed68c3c9ed167374bec6c5ec0418227b0c6b60bece87a2e2988934b5" {
		t.Fatalf("worker runtime lost the OCI digest prefix: %#v", runtime["image_digest"])
	}
	if _, present := runtime["fixed_items"]; present {
		t.Fatal("worker runtime contains a field outside worker.task.v1.runtime")
	}
	streams, ok := runtime["random_streams"].(map[string]any)
	if !ok {
		t.Fatalf("random_streams has unexpected type: %T", runtime["random_streams"])
	}
	if streams["generator"] != "MT19937_TWISTER_COMPAT" || streams["seed_mapping_version"] != "reference-anchor-v1" || streams["public_anchor_seed"] != int64(2126) {
		t.Fatalf("unexpected frozen random stream metadata: %#v", streams)
	}
	for field, expected := range map[string]map[string]int64{
		"base_center_seed_by_agent":       {"1": 2027, "2": 2028, "3": 2029},
		"transition_center_seed_by_agent": {"1": 2047, "2": 2048, "3": 2049},
		"boundary_seed_by_agent":          {"1": 2067, "2": 2068, "3": 2069},
	} {
		observed, ok := streams[field].(map[string]int64)
		if !ok {
			t.Fatalf("%s has unexpected type: %T", field, streams[field])
		}
		for agent, seed := range expected {
			if observed[agent] != seed {
				t.Fatalf("%s[%s] = %d, want %d", field, agent, observed[agent], seed)
			}
		}
	}
}

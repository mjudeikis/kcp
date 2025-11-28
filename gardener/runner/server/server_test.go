package server

import (
	"encoding/json"
	"testing"

	"github.com/gardener/gardener/pkg/apis/core/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestCreateJSONPatch(t *testing.T) {
	tests := []struct {
		name     string
		original *v1beta1.Shoot
		mutated  *v1beta1.Shoot
		expected []map[string]any
	}{
		{
			name: "no changes",
			original: &v1beta1.Shoot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-shoot",
					Namespace: "test-ns",
				},
				Spec: v1beta1.ShootSpec{
					CloudProfileName: ptr.To("test-profile"),
					Region:           "us-west-1",
				},
			},
			mutated: &v1beta1.Shoot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-shoot",
					Namespace: "test-ns",
				},
				Spec: v1beta1.ShootSpec{
					CloudProfileName: ptr.To("test-profile"),
					Region:           "us-west-1",
				},
			},
			expected: []map[string]any{},
		},
		{
			name: "spec field changed",
			original: &v1beta1.Shoot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-shoot",
					Namespace: "test-ns",
				},
				Spec: v1beta1.ShootSpec{
					CloudProfileName: ptr.To("old-profile"),
					Region:           "us-west-1",
				},
			},
			mutated: &v1beta1.Shoot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-shoot",
					Namespace: "test-ns",
				},
				Spec: v1beta1.ShootSpec{
					CloudProfileName: ptr.To("new-profile"),
					Region:           "us-west-1",
				},
			},
			expected: []map[string]any{
				{
					"op":    "replace",
					"path":  "/spec/cloudProfileName",
					"value": "new-profile",
				},
			},
		},
		{
			name: "field added",
			original: &v1beta1.Shoot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-shoot",
					Namespace: "test-ns",
				},
				Spec: v1beta1.ShootSpec{
					CloudProfileName: ptr.To("test-profile"),
				},
			},
			mutated: &v1beta1.Shoot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-shoot",
					Namespace: "test-ns",
				},
				Spec: v1beta1.ShootSpec{
					CloudProfileName: ptr.To("test-profile"),
					Region:           "us-west-1",
				},
			},
			expected: []map[string]any{
				{
					"op":    "replace",
					"path":  "/spec/region",
					"value": "us-west-1",
				},
			},
		},
		{
			name: "annotation added",
			original: &v1beta1.Shoot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-shoot",
					Namespace: "test-ns",
				},
			},
			mutated: &v1beta1.Shoot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-shoot",
					Namespace: "test-ns",
					Annotations: map[string]string{
						"mutation.webhook/applied": "true",
					},
				},
			},
			expected: []map[string]any{
				{
					"op":   "add",
					"path": "/metadata/annotations",
					"value": map[string]any{
						"mutation.webhook/applied": "true",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patchBytes, err := createJSONPatch(tt.original, tt.mutated)
			if err != nil {
				t.Fatalf("createJSONPatch() error = %v", err)
			}

			var patches []map[string]any
			err = json.Unmarshal(patchBytes, &patches)
			if err != nil {
				t.Fatalf("failed to unmarshal patches: %v", err)
			}

			if len(patches) != len(tt.expected) {
				t.Errorf("expected %d patches, got %d", len(tt.expected), len(patches))
				return
			}

			// Convert expected to JSON and back for consistent comparison
			expectedBytes, _ := json.Marshal(tt.expected)
			var normalizedExpected []map[string]any
			json.Unmarshal(expectedBytes, &normalizedExpected)

			actualBytes, _ := json.Marshal(patches)
			expectedBytesNorm, _ := json.Marshal(normalizedExpected)

			if string(actualBytes) != string(expectedBytesNorm) {
				t.Errorf("patches don't match\nexpected: %s\nactual:   %s",
					string(expectedBytesNorm), string(actualBytes))
			}
		})
	}
}

func TestDeepEqual(t *testing.T) {
	tests := []struct {
		name     string
		a, b     any
		expected bool
	}{
		{
			name:     "equal strings",
			a:        "hello",
			b:        "hello",
			expected: true,
		},
		{
			name:     "different strings",
			a:        "hello",
			b:        "world",
			expected: false,
		},
		{
			name:     "equal maps",
			a:        map[string]any{"key": "value"},
			b:        map[string]any{"key": "value"},
			expected: true,
		},
		{
			name:     "different maps",
			a:        map[string]any{"key": "value1"},
			b:        map[string]any{"key": "value2"},
			expected: false,
		},
		{
			name:     "equal nested maps",
			a:        map[string]any{"outer": map[string]any{"inner": "value"}},
			b:        map[string]any{"outer": map[string]any{"inner": "value"}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deepEqual(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("deepEqual() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

/*
Copyright 2025 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mutators

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func loadTestData(t *testing.T) *unstructured.Unstructured {
	testDataPath := filepath.Join("testdata", "shoot_test_data.yaml")
	data, err := os.ReadFile(testDataPath)
	require.NoError(t, err, "failed to read test data file")

	var obj unstructured.Unstructured
	err = yaml.Unmarshal(data, &obj)
	require.NoError(t, err, "failed to unmarshal test data")

	return &obj
}

func createShootObject(name, namespace string, spec, status map[string]interface{}, finalizers []string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "core.gardener.cloud/v1beta1",
			"kind":       "Shoot",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
		},
	}

	if finalizers != nil {
		// Convert to []interface{} to match unstructured behavior
		interfaceFinalizers := make([]interface{}, len(finalizers))
		for i, f := range finalizers {
			interfaceFinalizers[i] = f
		}
		obj.Object["metadata"].(map[string]interface{})["finalizers"] = interfaceFinalizers
	}

	if spec != nil {
		obj.Object["spec"] = spec
	}

	if status != nil {
		obj.Object["status"] = status
	}

	return obj
}

func TestShootToConsumer(t *testing.T) {
	testData := loadTestData(t)

	tests := []struct {
		name     string
		provider *unstructured.Unstructured
		consumer *unstructured.Unstructured
		expected *unstructured.Unstructured
	}{
		{
			name:     "nil consumer returns error",
			provider: &unstructured.Unstructured{},
			consumer: nil,
			expected: nil,
		},
		{
			name: "copy status from provider to consumer",
			provider: func() *unstructured.Unstructured {
				provider := testData.DeepCopy()
				// Keep only status for this test
				unstructured.RemoveNestedField(provider.Object, "spec")
				provider.SetName("test-shoot")
				provider.SetNamespace("garden-test")
				return provider
			}(),
			consumer: createShootObject("test-shoot", "garden-test", map[string]interface{}{
				"region": "test-region",
			}, nil, nil),
			expected: func() *unstructured.Unstructured {
				expected := createShootObject("test-shoot", "garden-test", map[string]interface{}{
					"region": "test-region",
				}, nil, []string{"gardener"}) // Finalizers will be copied too since they're in preserveToProvider
				// Copy status from test data
				status, _, _ := unstructured.NestedMap(testData.Object, "status")
				unstructured.SetNestedMap(expected.Object, status, "status")
				return expected
			}(),
		},
		{
			name: "copy preserved spec fields from provider to consumer",
			provider: func() *unstructured.Unstructured {
				provider := testData.DeepCopy()
				provider.SetName("test-shoot")
				provider.SetNamespace("garden-test")
				// Remove status to focus on spec fields
				unstructured.RemoveNestedField(provider.Object, "status")
				return provider
			}(),
			consumer: createShootObject("test-shoot", "garden-test", map[string]interface{}{
				"region": "original-region",
				"networking": map[string]interface{}{
					"pods":     "10.1.0.0/16",
					"services": "10.2.0.0/16",
				},
			}, nil, nil),
			expected: func() *unstructured.Unstructured {
				// Start with consumer spec and override with preserved fields
				expected := createShootObject("test-shoot", "garden-test", map[string]interface{}{
					"region": "original-region",
					"networking": map[string]interface{}{
						"pods":     "10.1.0.0/16",
						"services": "10.2.0.0/16",
					},
				}, nil, []string{"gardener"})

				// Apply preserved fields from test data
				cloudProfile, _, _ := unstructured.NestedMap(testData.Object, "spec", "cloudProfile")
				unstructured.SetNestedMap(expected.Object, cloudProfile, "spec", "cloudProfile")

				region, _, _ := unstructured.NestedString(testData.Object, "spec", "region")
				unstructured.SetNestedField(expected.Object, region, "spec", "region")

				dns, _, _ := unstructured.NestedMap(testData.Object, "spec", "dns")
				unstructured.SetNestedMap(expected.Object, dns, "spec", "dns")

				networking, _, _ := unstructured.NestedMap(testData.Object, "spec", "networking")
				pods, _, _ := unstructured.NestedString(networking, "pods")
				services, _, _ := unstructured.NestedString(networking, "services")
				unstructured.SetNestedField(expected.Object, pods, "spec", "networking", "pods")
				unstructured.SetNestedField(expected.Object, services, "spec", "networking", "services")

				return expected
			}(),
		},
		{
			name: "copy both status and preserved spec fields",
			provider: func() *unstructured.Unstructured {
				provider := testData.DeepCopy()
				provider.SetName("test-shoot")
				provider.SetNamespace("garden-test")
				return provider
			}(),
			consumer: createShootObject("test-shoot", "garden-test", map[string]interface{}{
				"region": "consumer-region",
			}, nil, nil),
			expected: func() *unstructured.Unstructured {
				expected := createShootObject("test-shoot", "garden-test", map[string]interface{}{
					"region": "consumer-region",
				}, nil, []string{"gardener"})

				// Apply preserved fields from test data
				cloudProfile, _, _ := unstructured.NestedMap(testData.Object, "spec", "cloudProfile")
				unstructured.SetNestedMap(expected.Object, cloudProfile, "spec", "cloudProfile")

				region, _, _ := unstructured.NestedString(testData.Object, "spec", "region")
				unstructured.SetNestedField(expected.Object, region, "spec", "region")

				dns, _, _ := unstructured.NestedMap(testData.Object, "spec", "dns")
				unstructured.SetNestedMap(expected.Object, dns, "spec", "dns")

				networking, _, _ := unstructured.NestedMap(testData.Object, "spec", "networking")
				pods, _, _ := unstructured.NestedString(networking, "pods")
				services, _, _ := unstructured.NestedString(networking, "services")
				unstructured.SetNestedField(expected.Object, pods, "spec", "networking", "pods")
				unstructured.SetNestedField(expected.Object, services, "spec", "networking", "services")

				// Add status from test data
				status, _, _ := unstructured.NestedMap(testData.Object, "status")
				unstructured.SetNestedMap(expected.Object, status, "status")

				return expected
			}(),
		},
		{
			name: "provider without status does not modify consumer",
			provider: func() *unstructured.Unstructured {
				provider := testData.DeepCopy()
				provider.SetName("test-shoot")
				provider.SetNamespace("garden-test")
				// Remove status and most spec fields to test minimal case
				unstructured.RemoveNestedField(provider.Object, "status")
				unstructured.RemoveNestedField(provider.Object, "spec", "cloudProfile")
				unstructured.RemoveNestedField(provider.Object, "spec", "dns")
				unstructured.RemoveNestedField(provider.Object, "spec", "networking")
				unstructured.RemoveNestedField(provider.Object, "metadata", "finalizers")
				return provider
			}(),
			consumer: createShootObject("test-shoot", "garden-test", map[string]interface{}{
				"region": "consumer-region",
			}, nil, nil),
			expected: func() *unstructured.Unstructured {
				expected := createShootObject("test-shoot", "garden-test", map[string]interface{}{
					"region": "consumer-region",
				}, nil, nil)

				// Only the region from provider should be applied
				region, _, _ := unstructured.NestedString(testData.Object, "spec", "region")
				unstructured.SetNestedField(expected.Object, region, "spec", "region")

				return expected
			}(),
		},
		{
			name:     "real-world example with complex nested structures",
			provider: testData.DeepCopy(), // Use full test data as provider
			consumer: createShootObject("local1", "garden-local", map[string]interface{}{
				"kubernetes": map[string]interface{}{
					"version": "1.33.0", // Different version than provider
				},
				"networking": map[string]interface{}{
					"pods":     "10.1.0.0/16", // Different than provider
					"services": "10.2.0.0/16", // Different than provider
				},
			}, nil, nil),
			expected: func() *unstructured.Unstructured {
				// Start with consumer's non-preserved fields
				expected := createShootObject("local1", "garden-local", map[string]interface{}{
					"kubernetes": map[string]interface{}{
						"version": "1.33.0", // Keep consumer's kubernetes version
					},
				}, nil, []string{"gardener"})

				// Add preserved fields from provider (test data)
				cloudProfile, _, _ := unstructured.NestedMap(testData.Object, "spec", "cloudProfile")
				unstructured.SetNestedMap(expected.Object, cloudProfile, "spec", "cloudProfile")

				region, _, _ := unstructured.NestedString(testData.Object, "spec", "region")
				unstructured.SetNestedField(expected.Object, region, "spec", "region")

				dns, _, _ := unstructured.NestedMap(testData.Object, "spec", "dns")
				unstructured.SetNestedMap(expected.Object, dns, "spec", "dns")

				// Override networking with provider's preserved values
				networking, _, _ := unstructured.NestedMap(testData.Object, "spec", "networking")
				pods, _, _ := unstructured.NestedString(networking, "pods")
				services, _, _ := unstructured.NestedString(networking, "services")
				unstructured.SetNestedField(expected.Object, pods, "spec", "networking", "pods")
				unstructured.SetNestedField(expected.Object, services, "spec", "networking", "services")

				// Add full status from provider
				status, _, _ := unstructured.NestedMap(testData.Object, "status")
				unstructured.SetNestedMap(expected.Object, status, "status")

				return expected
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nil consumer returns error" {
				err := ShootToConsumer(tt.provider, tt.consumer)
				require.Error(t, err)
				require.Contains(t, err.Error(), "consumer object is nil")
				return
			}

			// Make a copy of consumer to test in-place modification
			consumerCopy := tt.consumer.DeepCopy()
			err := ShootToConsumer(tt.provider, consumerCopy)
			require.NoError(t, err)
			require.Equal(t, tt.expected, consumerCopy)
		})
	}
}

func TestShootToConsumer_ErrorHandling(t *testing.T) {
	t.Run("malformed provider status causes error", func(t *testing.T) {
		provider := createShootObject("test", "test", nil, nil, nil)
		provider.Object["status"] = "invalid-status-type" // status should be a map, not a string

		consumer := createShootObject("test", "test", map[string]interface{}{}, nil, nil)

		err := ShootToConsumer(provider, consumer)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to get status from provider")
	})

	t.Run("malformed consumer object during field copy", func(t *testing.T) {
		// Create provider with finalizers (a preserved field)
		provider := createShootObject("test", "test", nil, nil, []string{"gardener"})

		// Create consumer with invalid metadata structure to trigger SetNestedField error
		consumer := createShootObject("test", "test", nil, nil, nil)
		consumer.Object["metadata"] = "invalid-metadata-type" // Should be map, not string

		err := ShootToConsumer(provider, consumer)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to set field")
	})
}

func TestParseFieldPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "single field",
			input:    "metadata",
			expected: []string{"metadata"},
		},
		{
			name:     "nested field",
			input:    "metadata.finalizers",
			expected: []string{"metadata", "finalizers"},
		},
		{
			name:     "deeply nested field",
			input:    "spec.networking.pods",
			expected: []string{"spec", "networking", "pods"},
		},
		{
			name:     "empty string",
			input:    "",
			expected: []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseFieldPath(tt.input)
			require.Equal(t, tt.expected, result)
		})
	}
}

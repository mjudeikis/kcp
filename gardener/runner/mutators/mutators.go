package mutators

import (
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// mutators takes object from the consumer logicalcluster (kcp) and transforms it to the provider cluster.
// It takes uses converters to transform the object as needed. In general spec is always synced to provider,
// while status is synced back to consumer.
// There is caviate that if provider does not conform to the kuberentes spec/status good practices, some fields
// are propagated back to consumer in spec (out of band mutation) and vice versa.

// So rule of the thumb is: Spec is synced to provider, Status is synced to consumer. With exception of
// out of band mutations.

var (
	// ConsumerClusterAnnotation stores the cluster name where the object originated
	ConsumerClusterAnnotation = "syncer.kcp.io/origin-cluster"
)

type Mutator struct {
	ToProvider func(consumer, provider *unstructured.Unstructured) error
	ToConsumer func(provider, consumer *unstructured.Unstructured) error
}

var (
	// Add more mutators as needed
	Mutators = map[schema.GroupVersionKind]Mutator{
		{
			Group:   "core.gardener.cloud",
			Version: "v1beta1",
			Kind:    "Shoot",
		}: {ToProvider: ShootToProvider, ToConsumer: ShootToConsumer},
	}
)

func GetMutator(gvk schema.GroupVersionKind) (Mutator, error) {
	mutator, found := Mutators[gvk]
	if !found {
		return Mutator{}, fmt.Errorf("no mutator found for GVK %s", gvk.String())
	}
	return mutator, nil
}

// Fields that should be preserved during mutation from consumer to provider and vice versa.
// These fields are mutated in spec by the provider cluster and should not be overwritten by the consumer cluster.
var preserveToProvider = []string{
	"metadata.finalizers",
	"spec.cloudProfile",
	"spec.region",
	"spec.dns",
	"spec.networking.pods",
	"spec.networking.services",
}

// ShootToProvider mutates shoot object from consumer to provider format.
// It takes fields from provider first, which are "preserved", then copies them ontop of the consumer spec.
// Finally it copies the entire spec from consumer to provider, effectively overwriting everything except preserved fields.
// It modified object in place so caller should pass a copy of the original object if needed.
func ShootToProvider(consumer, provider *unstructured.Unstructured) error {
	if consumer == nil {
		return fmt.Errorf("consumer object is nil")
	}

	// If provider object exists, check if any preserveToProvider fields have changed
	// and copy them back to consumer spec
	if provider != nil {
		for _, fieldPath := range preserveToProvider {
			if providerValue, found, err := unstructured.NestedFieldCopy(provider.Object, parseFieldPath(fieldPath)...); err != nil {
				return fmt.Errorf("failed to get field %s from provider: %w", fieldPath, err)
			} else if found {
				// Copy provider field value back to consumer spec
				if err := unstructured.SetNestedField(consumer.Object, providerValue, parseFieldPath(fieldPath)...); err != nil {
					return fmt.Errorf("failed to set field %s in consumer: %w", fieldPath, err)
				}
			}
		}
	} else {
		// If provider does not exist, initialize a new provider object
		provider = &unstructured.Unstructured{}
		provider.SetGroupVersionKind(consumer.GetObjectKind().GroupVersionKind())
		provider.SetNamespace(consumer.GetNamespace())
		provider.SetName(consumer.GetName())
	}

	// Add origin cluster annotation to preserve source cluster information
	addLogicalClusterAnnotation(consumer, provider)

	// Copy spec from consumer to provider object
	if spec, found, err := unstructured.NestedMap(consumer.Object, "spec"); err != nil {
		return fmt.Errorf("failed to get spec from consumer object: %w", err)
	} else if found {
		if err := unstructured.SetNestedMap(provider.Object, spec, "spec"); err != nil {
			return fmt.Errorf("failed to set spec in provider object: %w", err)
		}
	}

	// Drop status from provider object to avoid noise
	delete(provider.Object, "status")

	return nil
}

// ShootToConsumer mutates shoot object from provider to consumer format.
// It modified object in place so caller should pass a copy of the original object if needed.
func ShootToConsumer(provider, consumer *unstructured.Unstructured) error {
	if consumer == nil {
		return fmt.Errorf("consumer object is nil")
	}

	// Copy status from consumer to provider object
	if status, found, err := unstructured.NestedMap(provider.Object, "status"); err != nil {
		return fmt.Errorf("failed to get spec from consumer object: %w", err)
	} else if found {
		if err := unstructured.SetNestedMap(consumer.Object, status, "status"); err != nil {
			return fmt.Errorf("failed to set spec in provider object: %w", err)
		}
	}

	return nil
}

// parseFieldPath converts a dot-separated field path to a slice of strings
// for use with unstructured.NestedField functions
func parseFieldPath(fieldPath string) []string {
	return strings.Split(fieldPath, ".")
}

// addLogicalClusterAnnotation adds the origin cluster annotation to the provider object
func addLogicalClusterAnnotation(consumerObj, providerObj *unstructured.Unstructured) {
	// Add origin cluster annotation to preserve source cluster information
	annotations := consumerObj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	cluster := logicalcluster.From(consumerObj)

	annotations[ConsumerClusterAnnotation] = cluster.String()
	providerObj.SetAnnotations(annotations)
}

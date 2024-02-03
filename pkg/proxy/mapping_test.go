package proxy

import (
	"reflect"
	"testing"
)

func TestSortMappings(t *testing.T) {
	mappings := []httpHandlerMapping{
		{weight: 3},
		{weight: 1},
		{weight: 2},
		{weight: 10},
	}

	expected := []httpHandlerMapping{
		{weight: 10},
		{weight: 3},
		{weight: 2},
		{weight: 1},
	}

	sortedMappings := sortMappings(mappings)

	if !reflect.DeepEqual(sortedMappings, expected) {
		t.Errorf("Expected %v, but got %v", expected, sortedMappings)
	}
}

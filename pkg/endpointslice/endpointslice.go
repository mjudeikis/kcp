/*
Copyright 2025 The kcp Authors.

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

package endpointslice

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type DeserializeErrorCode int

const (
	NoEndpoints DeserializeErrorCode = iota
	BadObject
)

type DeserializeError struct {
	Code DeserializeErrorCode
	Err  error
}

func (e *DeserializeError) Error() string {
	return e.Err.Error()
}

// ListURLsFromUnstructured retrieves list of endpoint URLs from an unstructured object.
// The URLs are expected to be present at `.status.endpoints[].url` path inside the object.
func ListURLsFromUnstructured(endpointSlice unstructured.Unstructured) ([]string, error) {
	statusRaw, found, err := unstructured.NestedFieldNoCopy(endpointSlice.Object, "status")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &DeserializeError{
			Code: NoEndpoints,
			Err:  fmt.Errorf("missing status"),
		}
	}
	status, ok := statusRaw.(map[string]interface{})
	if !ok {
		return nil, &DeserializeError{
			Code: BadObject,
			Err:  fmt.Errorf("status field is of type %T, expected map[string]interface{}", statusRaw),
		}
	}

	endpointsRaw, found, err := unstructured.NestedFieldNoCopy(status, "endpoints")
	if err != nil {
		return nil, err
	}
	if !found || endpointsRaw == nil {
		return nil, &DeserializeError{
			Code: NoEndpoints,
			Err:  fmt.Errorf("missing status.endpoints"),
		}
	}
	endpoints, ok := endpointsRaw.([]interface{})
	if !ok {
		return nil, &DeserializeError{
			Code: BadObject,
			Err:  fmt.Errorf("status.endpoints field is of type %T, expected map[string]interface{}", statusRaw),
		}
	}

	urls := make([]string, 0, len(endpoints))
	for i, ep := range endpoints {
		endpointMap, ok := ep.(map[string]interface{})
		if !ok {
			return nil, &DeserializeError{
				Code: BadObject,
				Err:  fmt.Errorf("endpoint at index %d is not an object", i),
			}
		}

		url, found, err := unstructured.NestedString(endpointMap, "url")
		if err != nil {
			return nil, fmt.Errorf("failed to get url from endpoint at index %d: %w", i, err)
		}
		if !found {
			return nil, &DeserializeError{
				Code: BadObject,
				Err:  fmt.Errorf("missing url in endpoint at index %d", i),
			}
		}

		urls = append(urls, url)
	}

	return urls, nil
}

// FindOneURL picks the URL this shard should use out of the ones an endpoint
// slice advertises.
//
// The prefix is the shard's own virtual workspace URL, and it is a *selection*
// rule: a slice filled in by kcp's own controllers carries one URL per shard,
// all composed from each shard's virtual workspace URL, and this is how a shard
// finds its own. It is not an authorization rule -- what a shard may talk to is
// decided by whoever gets to write the URL, and by the egress policy applied
// when dialling it.
//
// A slice may equally advertise a single URL meant for every shard, which is
// what a provider running one virtual workspace for the whole installation
// publishes. There is nothing to select in that case, so the prefix does not
// apply and the URL is used as it stands. Anything else -- several URLs, none
// of them this shard's -- is a slice that does not describe this shard, and
// that is an error rather than a guess.
func FindOneURL(prefix string, urls []string) (string, error) {
	var matches []string
	for _, url := range urls {
		if strings.HasPrefix(url, prefix) {
			matches = append(matches, url)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		// A single URL is a singleton: one virtual workspace serving every
		// shard, so there is no per-shard choice to make.
		if len(urls) == 1 {
			return urls[0], nil
		}
		if len(urls) == 0 {
			return "", fmt.Errorf("no URLs to choose from")
		}
		return "", fmt.Errorf("none of the URLs %v are for this shard (prefix %q), and there is more than one to choose from", urls, prefix)
	default:
		return "", fmt.Errorf("ambiguous URLs %v with prefix %q", matches, prefix)
	}
}

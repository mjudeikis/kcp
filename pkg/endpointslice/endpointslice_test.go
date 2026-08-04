/*
Copyright 2026 The kcp Authors.

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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindOneURL(t *testing.T) {
	t.Parallel()

	const thisShard = "https://shard-1.example.com"

	for _, tc := range []struct {
		name    string
		urls    []string
		want    string
		wantErr string
	}{
		{
			name: "one URL per shard, ours among them",
			urls: []string{
				"https://shard-0.example.com/services/replication/a/b",
				"https://shard-1.example.com/services/replication/a/b",
			},
			want: "https://shard-1.example.com/services/replication/a/b",
		},
		{
			name: "a single URL for every shard is used as it stands",
			urls: []string{"https://ephemeral.example.com/services/apiexport/a/b"},
			want: "https://ephemeral.example.com/services/apiexport/a/b",
		},
		{
			name: "a single URL that happens to be ours",
			urls: []string{"https://shard-1.example.com/services/replication/a/b"},
			want: "https://shard-1.example.com/services/replication/a/b",
		},
		{
			name: "several URLs, none of them ours, is not a guess",
			urls: []string{
				"https://shard-0.example.com/services/replication/a/b",
				"https://shard-2.example.com/services/replication/a/b",
			},
			wantErr: "none of the URLs",
		},
		{
			name:    "no URLs at all",
			urls:    nil,
			wantErr: "no URLs to choose from",
		},
		{
			name: "two URLs for this shard is ambiguous",
			urls: []string{
				"https://shard-1.example.com/services/replication/a/b",
				"https://shard-1.example.com/services/replication/c/d",
			},
			wantErr: "ambiguous",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := FindOneURL(thisShard, tc.urls)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

/*
Copyright 2023 The KCP Authors.

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

package apiexportendpointslice

import (
	"context"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIExportEndpointSliceWithPartition(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := framework.SharedKcpServer(t)

	// Create Organization and Workspaces
	orgPath, _ := framework.NewOrganizationFixture(t, server)
	exportClusterPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)
	partitionClusterPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)

	var err error
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	export := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-export",
		},
	}

	slice := &apisv1alpha1.APIExportEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "my-slice",
		},
		Spec: apisv1alpha1.APIExportEndpointSliceSpec{
			APIExport: apisv1alpha1.ExportBindingReference{
				Path: exportClusterPath.String(),
				Name: export.Name,
			},
		},
	}

	partition := &topologyv1alpha1.Partition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-partition",
		},
		Spec: topologyv1alpha1.PartitionSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"region": "apiexportendpointslice-test-region",
				},
			},
		},
	}

	t.Logf("Creating an APIExportEndpointSlice with reference to a nonexistent APIExport")
	sliceClient := kcpClusterClient.ApisV1alpha1().APIExportEndpointSlices()

	_, err = sliceClient.Cluster(partitionClusterPath).Create(ctx, slice, metav1.CreateOptions{})
	require.True(t, apierrors.IsForbidden(err), "no error creating APIExportEndpointSlice (admission should have declined it)")
	sliceList, err := sliceClient.Cluster(partitionClusterPath).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing APIExportEndpointSlice")
	require.True(t, len(sliceList.Items) == 0, "not expecting any APIExportEndpointSlice")

	t.Logf("Creating the missing APIExport")
	exportClient := kcpClusterClient.ApisV1alpha1().APIExports()
	_, err = exportClient.Cluster(exportClusterPath).Create(ctx, export, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport")

	var sliceName string
	t.Logf("Retrying to create the APIExportEndpointSlice after the APIExport has been created")
	framework.Eventually(t, func() (bool, string) {
		created, err := sliceClient.Cluster(partitionClusterPath).Create(ctx, slice, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		sliceName = created.Name
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected APIExportEndpointSlice creation to succeed")

	framework.Eventually(t, func() (bool, string) {
		slice, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)

		if conditions.IsTrue(slice, apisv1alpha1.APIExportValid) && conditions.IsTrue(slice, apisv1alpha1.APIExportEndpointSliceReadyForURLs) {
			return true, ""
		}

		return false, spew.Sdump(slice.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid APIExport")

	t.Logf("Adding a Partition to the APIExportEndpointSlice")
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		slice, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		slice.Spec.Partition = partition.Name
		_, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Update(ctx, slice, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err, "error updating APIExportEndpointSlice")

	framework.Eventually(t, func() (bool, string) {
		slice, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if conditions.IsFalse(slice, apisv1alpha1.PartitionValid) && conditions.GetReason(slice, apisv1alpha1.PartitionValid) == apisv1alpha1.PartitionInvalidReferenceReason {
			return true, ""
		}

		return false, spew.Sdump(slice.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected missing Partition")
	require.True(t, len(slice.Status.APIExportEndpoints) == 0, "not expecting any endpoint")
	require.True(t, conditions.IsFalse(slice, apisv1alpha1.APIExportEndpointSliceReadyForURLs), "expecting URLs not ready condition")

	t.Logf("Creating the missing Partition")
	partitionClient := kcpClusterClient.TopologyV1alpha1().Partitions()
	_, err = partitionClient.Cluster(partitionClusterPath).Create(ctx, partition, metav1.CreateOptions{})
	require.NoError(t, err, "error creating Partition")

	framework.Eventually(t, func() (bool, string) {
		slice, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if conditions.IsTrue(slice, apisv1alpha1.PartitionValid) {
			return true, ""
		}

		return false, spew.Sdump(slice.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid Partition")

	t.Logf("Checking that no endpoint has been populated")
	require.True(t, len(slice.Status.APIExportEndpoints) == 0, "not expecting any endpoint")
	require.True(t, conditions.IsTrue(slice, apisv1alpha1.APIExportEndpointSliceReadyForURLs), "expecting the URLs ready condition")
}

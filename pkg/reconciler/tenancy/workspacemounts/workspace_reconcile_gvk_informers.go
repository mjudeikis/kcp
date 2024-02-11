/*
Copyright 2021 The KCP Authors.

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

package workspacemounts

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/runtime/schema"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

// gvkInformersReconciler is a collection of informers for the gvk used by mountpoints to trigger requeue on changes
type gvkInformersReconciler struct {
	setupGVKInformer func(gvk schema.GroupVersionKind) error
	requeueWorkspace func(cluster logicalcluster.Path, workspace tenancyv1alpha1.Workspace) error
}

func (r *gvkInformersReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	if workspace.Annotations == nil {
		workspace.Annotations = map[string]string{}
	}
	var mount *tenancyv1alpha1.Mount
	if v, ok := workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey]; ok {
		var err error
		mount, err = tenancyv1alpha1.ParseTenancyMountAnnotation(v)
		if err != nil {
			return reconcileStatusStopAndRequeue, err
		}
	}

	if mount == nil {
		// no mount annotation, nothing to do
		return reconcileStatusContinue, nil
	}

	gvk := mount.MountSpec.Reference.GroupVersionKind()

	if err := r.setupGVKInformer(gvk); err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	return reconcileStatusContinue, nil
}

func (r *gvkInformersReconciler) enqueueGVK(old, new interface{}) {
	if old == nil || new == nil {
		return
	}
}

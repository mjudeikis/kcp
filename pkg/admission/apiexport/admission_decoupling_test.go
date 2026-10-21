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

package apiexport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
)

func crdResource(name, group string) apisv1alpha2.ResourceSchema {
	return apisv1alpha2.ResourceSchema{
		Name: name, Group: group, Schema: "today." + name + "." + group,
		Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
	}
}

func TestDecouplingValidation(t *testing.T) {
	t.Parallel()

	const cluster = "ws-compute"
	export := func(mutate func(*apisv1alpha2.APIExport)) *apisv1alpha2.APIExport {
		ae := &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: "compute", Annotations: map[string]string{logicalcluster.AnnotationKey: cluster}},
			Spec:       apisv1alpha2.APIExportSpec{Resources: []apisv1alpha2.ResourceSchema{crdResource("vms", "compute.io")}},
		}
		if mutate != nil {
			mutate(ae)
		}
		return ae
	}
	heldBy := func(cluster, name string) func(string) ([]*apisv1alpha2.APIExport, error) {
		return func(string) ([]*apisv1alpha2.APIExport, error) {
			return []*apisv1alpha2.APIExport{{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{logicalcluster.AnnotationKey: cluster}}}}, nil
		}
	}

	cases := map[string]struct {
		obj, old   *apisv1alpha2.APIExport
		decoupling bool
		holders    func(string) ([]*apisv1alpha2.APIExport, error)
		registry   func(string) (*adminv1alpha1.APIIdentity, error)
		wantErr    string
	}{
		"claim without identity is rejected": {
			obj: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.PermissionClaims = []apisv1alpha2.PermissionClaim{{GroupResource: apisv1alpha2.GroupResource{Group: "networking.io", Resource: "vnets"}, Verbs: []string{"get"}}}
			}),
			decoupling: true,
			wantErr:    "identityHash is required",
		},
		"identity value must carry the cluster prefix": {
			obj: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Identity = &apisv1alpha2.Identity{Value: "other-abc"}
			}),
			wantErr: `must be prefixed with this export's logical cluster name "ws-compute"`,
		},
		"identity value with the cluster prefix": {
			obj: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Identity = &apisv1alpha2.Identity{Value: cluster + "-abc"}
			}),
			holders: func(string) ([]*apisv1alpha2.APIExport, error) { return nil, nil },
			registry: func(string) (*adminv1alpha1.APIIdentity, error) {
				return nil, apierrors.NewNotFound(adminv1alpha1.Resource("apiidentities"), "x")
			},
		},
		"identity value held by another export": {
			obj: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Identity = &apisv1alpha2.Identity{Value: cluster + "-abc"}
			}),
			holders: heldBy(cluster, "other"),
			wantErr: "is held by APIExport ws-compute:other",
		},
		"identity value held by this export (re-create) is fine": {
			obj: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Identity = &apisv1alpha2.Identity{Value: cluster + "-abc"}
			}),
			holders: heldBy(cluster, "compute"),
		},
		"identity value recorded for another owner in the registry": {
			obj: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Identity = &apisv1alpha2.Identity{Value: cluster + "-abc"}
			}),
			registry: func(string) (*adminv1alpha1.APIIdentity, error) {
				return &adminv1alpha1.APIIdentity{Status: adminv1alpha1.APIIdentityStatus{Owner: &adminv1alpha1.ExportIdentityReference{Cluster: "ws-other", Export: "compute"}}}, nil
			},
			wantErr: "is held by APIExport ws-other|compute",
		},
		"identity value orphaned in the registry": {
			obj: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Identity = &apisv1alpha2.Identity{Value: cluster + "-abc"}
			}),
			registry: func(string) (*adminv1alpha1.APIIdentity, error) {
				id := &adminv1alpha1.APIIdentity{}
				conditions.MarkTrue(id, adminv1alpha1.APIIdentityOrphaned)
				return id, nil
			},
			wantErr: "is orphaned",
		},
		"identity value is immutable once the identity is assigned": {
			old: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Identity = &apisv1alpha2.Identity{Value: cluster + "-abc"}
				ae.Status.IdentityHash = cluster + "-abc"
			}),
			obj: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Identity = &apisv1alpha2.Identity{Value: cluster + "-def"}
				ae.Status.IdentityHash = cluster + "-abc"
			}),
			wantErr: "value is immutable once the export has an identity",
		},
		"unchanged identity value after assignment is fine": {
			old: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Identity = &apisv1alpha2.Identity{Value: cluster + "-abc"}
				ae.Status.IdentityHash = cluster + "-abc"
			}),
			obj: export(func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Identity = &apisv1alpha2.Identity{Value: cluster + "-abc"}
				ae.Status.IdentityHash = cluster + "-abc"
			}),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plugin := NewAPIExportAdmission(func(apis.GroupResource) bool { return false })
			plugin.decouplingEnabled = func() bool { return tc.decoupling }
			plugin.getAPIExportsByIdentity = tc.holders
			plugin.getAPIIdentity = tc.registry

			var attr admission.Attributes
			if tc.old != nil {
				attr = admission.NewAttributesRecord(helpers.ToUnstructuredOrDie(tc.obj), helpers.ToUnstructuredOrDie(tc.old),
					apisv1alpha2.Kind("APIExport").WithVersion("v1alpha2"), "", tc.obj.Name,
					apisv1alpha2.Resource("apiexports").WithVersion("v1alpha2"), "", admission.Update, &metav1.UpdateOptions{}, false, &user.DefaultInfo{})
			} else {
				attr = createAttr(tc.obj.Name, tc.obj, "APIExport", "apiexports")
			}
			ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: logicalcluster.Name(cluster)})
			err := plugin.Validate(ctx, attr, nil)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestStatusIdentityHashLegacyConversion(t *testing.T) {
	t.Parallel()

	export := func(hash string, legacyShared bool) runtime.Object {
		ae := &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: "cool-something"},
			Status:     apisv1alpha2.APIExportStatus{IdentityHash: hash},
		}
		if legacyShared {
			conditions.Set(ae, &conditionsv1alpha1.Condition{Type: apisv1alpha2.IdentityLegacyShared, Status: "True"})
		}
		return ae
	}
	attr := func(oldShared bool, u user.Info) admission.Attributes {
		return admission.NewAttributesRecord(
			helpers.ToUnstructuredOrDie(export("fresh", false)),
			helpers.ToUnstructuredOrDie(export("shared", oldShared)),
			apisv1alpha2.Kind("APIExport").WithVersion("v1alpha2"), "", "cool-something",
			apisv1alpha2.Resource("apiexports").WithVersion("v1alpha2"), "status",
			admission.Update, &metav1.UpdateOptions{}, false, u,
		)
	}
	masters := &user.DefaultInfo{Name: "system:apiserver", Groups: []string{user.SystemPrivilegedGroup}}
	plugin := NewAPIExportAdmission(func(apis.GroupResource) bool { return false })

	require.NoError(t, plugin.Validate(context.Background(), attr(true, masters), nil), "kcp may convert a legacy-shared identity")
	err := plugin.Validate(context.Background(), attr(false, masters), nil)
	require.Error(t, err, "without the IdentityLegacyShared condition the identity stays immutable")
	err = plugin.Validate(context.Background(), attr(true, &user.DefaultInfo{Name: "someone", Groups: []string{"system:authenticated"}}), nil)
	require.Error(t, err, "only kcp may convert")
}
